package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
	"gitee.com/kobeyoung81/nanoprotocol"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	hypervisorTunnelPingInterval = 45 * time.Second
	hypervisorTunnelPongWait     = 75 * time.Second
	// fileTunnelTimeout bounds how long a files request waits for
	// the hypervisor's local bridge (which itself allows 60s). Keeping the bus
	// wait at least as long prevents slow container responses from being cut off.
	fileTunnelTimeout = 60 * time.Second
)

// hypervisorTunnelWriteTimeout bounds a single tunnel frame write (bus ->
// hypervisor). Without it, a half-dead tunnel could block the forward
// indefinitely, never reaching the response timeout above. It is a variable
// (not a const) so tests can shrink it.
var hypervisorTunnelWriteTimeout = 10 * time.Second

// hypervisorConn is a registered hypervisor tunnel: the per-user container's
// persistent WebSocket back to the bus, plus the in-flight request map.
type hypervisorConn struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan *nanoprotocol.TunnelResponse
}

// writeJSONScoped writes v to conn with a bounded, self-clearing write
// deadline. Shared by all tunnel data writes so a stalled hypervisor cannot
// block the bus forever nor leak a deadline into later operations.
func writeJSONScoped(conn *websocket.Conn, timeout time.Duration, v any) error {
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	err := conn.WriteJSON(v)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

func (c *hypervisorConn) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return writeJSONScoped(c.conn, hypervisorTunnelWriteTimeout, v)
}

func (c *hypervisorConn) addPending(id string) chan *nanoprotocol.TunnelResponse {
	ch := make(chan *nanoprotocol.TunnelResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	return ch
}

func (c *hypervisorConn) removePending(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *hypervisorConn) resolvePending(resp *nanoprotocol.TunnelResponse) bool {
	c.mu.Lock()
	ch, ok := c.pending[resp.RequestID]
	if ok {
		delete(c.pending, resp.RequestID)
	}
	c.mu.Unlock()
	if ok {
		ch <- resp
		close(ch)
	}
	return ok
}

func (c *hypervisorConn) failAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}

// hypervisorTunnelRegistry maps tunnel names ("hypervisor-<userID>") to the
// per-user container's tunnel connection.
type hypervisorTunnelRegistry struct {
	mu    sync.RWMutex
	conns map[string]*hypervisorConn
}

func newHypervisorTunnelRegistry() *hypervisorTunnelRegistry {
	return &hypervisorTunnelRegistry{conns: make(map[string]*hypervisorConn)}
}

func (r *hypervisorTunnelRegistry) register(name string, c *hypervisorConn) {
	r.mu.Lock()
	r.conns[name] = c
	r.mu.Unlock()
}

func (r *hypervisorTunnelRegistry) unregister(name string, c *hypervisorConn) {
	r.mu.Lock()
	if r.conns[name] == c {
		delete(r.conns, name)
	}
	r.mu.Unlock()
	c.failAll()
}

func (r *hypervisorTunnelRegistry) lookup(name string) (*hypervisorConn, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.conns[name]
	return c, ok
}

// handleHypervisorTunnelWS accepts a per-user hypervisor's persistent
// WebSocket, registers it as a named tunnel, and routes tunnel_request/
// tunnel_response messages so the portal can reach the container's HTTP bridge
// through the bus (works for docker/swarm/k8s alike).
func (s *Server) handleHypervisorTunnelWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var reg nanoprotocol.Message
	if err := conn.ReadJSON(&reg); err != nil {
		return
	}
	if reg.Type != nanoprotocol.TypeTunnelRegister {
		_ = conn.WriteJSON(nanoprotocol.NewErrorMessage("expected tunnel_register"))
		return
	}
	var payload nanoprotocol.TunnelRegister
	if err := reg.DecodePayload(&payload); err != nil || payload.UserID == "" {
		_ = conn.WriteJSON(nanoprotocol.NewErrorMessage("invalid tunnel_register"))
		return
	}

	name := "hypervisor-" + payload.UserID
	hc := &hypervisorConn{conn: conn, pending: make(map[string]chan *nanoprotocol.TunnelResponse)}
	s.hypervisorTunnels.register(name, hc)
	defer s.hypervisorTunnels.unregister(name, hc)

	_ = conn.SetReadDeadline(time.Now().Add(hypervisorTunnelPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(hypervisorTunnelPongWait))
	})

	registered, _ := nanoprotocol.NewMessage(nanoprotocol.TypeTunnelRegistered, nanoprotocol.TunnelRegistered{})
	if err := hc.writeJSON(registered); err != nil {
		return
	}
	slog.Info("hypervisor tunnel registered", "user_id", payload.UserID)

	done := make(chan struct{})
	defer close(done)
	go s.tunnelKeepalive(conn, done)

	for {
		var msg nanoprotocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case nanoprotocol.TypeTunnelResponse:
			var resp nanoprotocol.TunnelResponse
			if err := msg.DecodePayload(&resp); err != nil {
				continue
			}
			hc.resolvePending(&resp)
		case nanoprotocol.TypeTunnelStreamOutput:
			var p nanoprotocol.TunnelStreamOutput
			if err := msg.DecodePayload(&p); err != nil {
				continue
			}
			s.routeTerminalOutput(p.StreamID, p.Data)
		case nanoprotocol.TypeTunnelStreamClosed:
			var p nanoprotocol.TunnelStreamClosed
			if err := msg.DecodePayload(&p); err != nil {
				continue
			}
			s.routeTerminalClosed(p.StreamID, p.Reason)
		}
	}
}

// tunnelKeepalive pings the hypervisor so idle tunnels survive intermediaries.
func (s *Server) tunnelKeepalive(conn *websocket.Conn, done <-chan struct{}) {
	ticker := time.NewTicker(hypervisorTunnelPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

// errTunnelNotRegistered lets the HTTP relay answer 404 ("the container is not
// connected here") instead of flattening every failure into 502.
var errTunnelNotRegistered = errors.New("hypervisor tunnel not registered")

// restForwardTunnelRequest relays one HTTP request through a tunnel. The
// workspace module calls it only for containers that still dial the bus; a
// request for a container that connected to the provisioner is itself relayed by
// forwardTunnelRequest, so the caller does not need to know where it landed.
func (s *Server) restForwardTunnelRequest(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tunnel name is required"})
		return
	}
	var req nanoprotocol.TunnelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.Method == "" || req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "method and path are required"})
		return
	}
	if req.RequestID == "" {
		req.RequestID = uuid.NewString()
	}
	resp, err := s.forwardTunnelRequest(name, req, fileTunnelTimeout)
	if err != nil {
		var status *provisionerclient.StatusError
		switch {
		case errors.As(err, &status) && status.Code == http.StatusNotFound:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "tunnel is not registered"})
		case errors.As(err, &status) && status.Code >= 400 && status.Code < 500:
			// The provisioner refused the request itself (413 for a body past
			// the inline limit, 429 for a busy container). Passing its answer
			// through keeps the reason; flattening it into 502 loses it.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status.Code)
			_, _ = io.WriteString(w, status.Body)
			return
		case errors.Is(err, errTunnelNotRegistered):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "tunnel is not registered"})
		default:
			slog.Warn("relay tunnel request failed", "tunnel", name, "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "tunnel forward failed"})
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// forwardTunnelRequest sends a tunnel_request to the named hypervisor tunnel
// and blocks for the matching tunnel_response.
func (s *Server) forwardTunnelRequest(name string, req nanoprotocol.TunnelRequest, timeout time.Duration) (*nanoprotocol.TunnelResponse, error) {
	hc, ok := s.hypervisorTunnels.lookup(name)
	if !ok {
		// Tunnels terminate at the provisioner now. Containers that still dial
		// the bus (every container until it is restarted onto the new image) are
		// served from the local registry above; the rest are relayed. This whole
		// path is the dual-track window and goes away with it.
		if s.provisioner != nil && s.provisioner.Enabled() {
			return s.provisioner.ForwardTunnel(context.Background(), name, req)
		}
		return nil, fmt.Errorf("%w for %s", errTunnelNotRegistered, name)
	}

	ch := hc.addPending(req.RequestID)
	defer hc.removePending(req.RequestID)

	msg, err := nanoprotocol.NewMessage(nanoprotocol.TypeTunnelRequest, req)
	if err != nil {
		return nil, err
	}
	if err := hc.writeJSON(msg); err != nil {
		return nil, fmt.Errorf("write tunnel request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp == nil {
			return nil, fmt.Errorf("tunnel closed before response")
		}
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("tunnel request timed out")
	}
}

// restFileContentGET tunnels a file read (download) to the owning
// hypervisor.
// restFileContentPUT tunnels a file write (upload) to the owning
// hypervisor, forwarding the raw request body.
// restFileContentDELETE tunnels a file delete to the owning
// hypervisor.
// restFileMkdir tunnels a directory creation to the owning
// hypervisor.
// restFileRename tunnels a file rename to the owning
// hypervisor.
// restFileCopy tunnels a file copy to the owning hypervisor.
// restTerminalList tunnels a live-terminal listing to the owning hypervisor.
func (s *Server) restTerminalList(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	s.forwardHypervisorRequest(w, r, agentID, http.MethodGet, "/api/v1/terminals/"+agentID, nil)
}

// restTerminalAttach creates (or re-attaches to) a terminal session on the
// owning hypervisor, forwarding the raw request body ({terminal_id, cols, rows}).
func (s *Server) restTerminalAttach(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
		return
	}
	s.forwardHypervisorRequest(w, r, agentID, http.MethodPost, "/api/v1/terminals/"+agentID+"/attach", body)
}

// restTerminalKill tunnels a terminal kill to the owning hypervisor.
func (s *Server) restTerminalKill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	terminalID := r.PathValue("terminalID")
	s.forwardHypervisorRequest(w, r, agentID, http.MethodDelete, "/api/v1/terminals/"+agentID+"/"+terminalID, nil)
}

// forwardHypervisorRequest resolves an agent to its owner and tunnels an HTTP
// request to that owner's hypervisor, mirroring the response back to the caller.
func (s *Server) forwardHypervisorRequest(w http.ResponseWriter, r *http.Request, agentID, method, path string, body []byte) {
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_id is required"})
		return
	}

	agent, err := s.lookupAgent(agentID)
	if err != nil {
		if errors.Is(err, errAgentNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	req := nanoprotocol.TunnelRequest{
		RequestID: uuid.New().String(),
		Method:    method,
		Path:      path,
		Body:      body,
	}
	resp, err := s.forwardTunnelRequest("hypervisor-"+agent.UserID, req, fileTunnelTimeout)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	for key, values := range resp.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}
