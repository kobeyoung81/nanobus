package server

// Agent sockets.
//
// Every agent process holds one WebSocket to the bus and runs the same message
// envelope on it that /api/v1/rpc carries (the handlers are literally the same:
// handleMessage). What the connection adds is the direction HTTP cannot take:
// the bus can tell an agent that work arrived.
//
// Two notices are pushed, neither carries state:
//
//   - inbound_ready, when a row lands in the inbound queue (tailer on every
//     replica; the replica holding the socket pushes). The agent then claims the
//     row with poll_inbound, which is the atomic step — so a duplicated notice
//     costs one query and a lost one is covered by the fallback poll.
//   - platform_event, forwarding bus_platform_events (config_changed,
//     agents_changed). This replaces the two SSE subscriptions each agent
//     process used to open.
//
// The socket is registered in this replica's memory only. No session affinity
// is needed: the notices come from the tailer, which every replica follows.

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/metrics"
	"gitee.com/kobeyoung81/nanobus/internal/stream"
	"gitee.com/kobeyoung81/nanoprotocol"
	"github.com/gorilla/websocket"
)

const (
	// agentSocketPingInterval keeps intermediaries from dropping an idle socket.
	agentSocketPingInterval = 30 * time.Second
	// agentSocketPongWait is how long a silent socket may stay open.
	agentSocketPongWait = 75 * time.Second
	// agentSocketRegistrarWait bounds the wait for the first frame.
	agentSocketRegisterWait = 10 * time.Second
	// agentSocketRate and agentSocketBurst bound one connection's request rate,
	// measured on the request itself — the HTTP rate limiter only sees the
	// handshake, so without this the socket would be a way around it.
	agentSocketRate  = 50.0
	agentSocketBurst = 100.0
)

// agentSocket is one registered agent connection.
type agentSocket struct {
	conn *websocket.Conn
	// write is the frame writer; tests replace it with a recorder.
	write   func(any) error
	writeMu sync.Mutex

	userID     string
	agentID    string // "" means every agent of the user (the hypervisor)
	components map[string]bool

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func newAgentSocket(conn *websocket.Conn, reg nanoprotocol.AgentSocketRegister) *agentSocket {
	components := map[string]bool{}
	for _, c := range reg.Components {
		components[c] = true
	}
	if len(components) == 0 {
		components[nanoprotocol.SocketComponentConfig] = true
		components[nanoprotocol.SocketComponentAgents] = true
	}
	return &agentSocket{
		conn: conn,
		write: func(v any) error {
			return writeJSONScoped(conn, hypervisorTunnelWriteTimeout, v)
		},
		userID:     reg.UserID,
		agentID:    reg.AgentID,
		components: components,
		tokens:     agentSocketBurst,
		last:       time.Now(),
	}
}

func (c *agentSocket) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.write == nil {
		return nil
	}
	return c.write(v)
}

// allow is a token bucket: agentSocketRate per second, agentSocketBurst up
// front. It answers whether one more request may run now.
func (c *agentSocket) allow(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last.IsZero() {
		c.last = now
	}
	c.tokens += now.Sub(c.last).Seconds() * agentSocketRate
	if c.tokens > agentSocketBurst {
		c.tokens = agentSocketBurst
	}
	c.last = now
	if c.tokens < 1 {
		return false
	}
	c.tokens--
	return true
}

// wants reports whether this socket subscribed to a notification component.
func (c *agentSocket) wants(component string) bool { return c.components[component] }

// covers reports whether a message addressed to agentID belongs to this socket.
// An empty agent on either side means "any agent of the user", matching the
// claim's filter (`agent_id IN (”, ?)`).
func (c *agentSocket) covers(agentID string) bool {
	return c.agentID == "" || agentID == "" || c.agentID == agentID
}

// agentSocketRegistry holds this replica's connections, grouped by user.
type agentSocketRegistry struct {
	mu     sync.RWMutex
	byUser map[string]map[*agentSocket]struct{}
}

func newAgentSocketRegistry() *agentSocketRegistry {
	return &agentSocketRegistry{byUser: make(map[string]map[*agentSocket]struct{})}
}

func (r *agentSocketRegistry) register(c *agentSocket) {
	r.mu.Lock()
	conns, ok := r.byUser[c.userID]
	if !ok {
		conns = make(map[*agentSocket]struct{})
		r.byUser[c.userID] = conns
	}
	conns[c] = struct{}{}
	r.mu.Unlock()
	metrics.AgentSockets.Set(float64(r.count()))
}

func (r *agentSocketRegistry) unregister(c *agentSocket) {
	r.mu.Lock()
	if conns, ok := r.byUser[c.userID]; ok {
		delete(conns, c)
		if len(conns) == 0 {
			delete(r.byUser, c.userID)
		}
	}
	r.mu.Unlock()
	metrics.AgentSockets.Set(float64(r.count()))
}

func (r *agentSocketRegistry) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, conns := range r.byUser {
		n += len(conns)
	}
	return n
}

// pushInbound tells the sockets of one user that a message is waiting.
func (r *agentSocketRegistry) pushInbound(notice stream.InboundNotice) {
	if notice.Status != "" && notice.Status != "pending" {
		return
	}
	msg, err := nanoprotocol.NewMessage(nanoprotocol.TypeInboundReady, nanoprotocol.InboundReady{Count: 1})
	if err != nil {
		return
	}
	r.mu.RLock()
	targets := make([]*agentSocket, 0, 2)
	for c := range r.byUser[notice.UserID] {
		if c.covers(notice.AgentID) {
			targets = append(targets, c)
		}
	}
	r.mu.RUnlock()
	for _, c := range targets {
		if err := c.writeJSON(msg); err != nil {
			slog.Debug("agent socket: inbound push failed", "user_id", c.userID, "agent_id", c.agentID, "error", err)
			continue
		}
		metrics.AgentSocketPushes.WithLabelValues("inbound").Inc()
	}
}

// pushPlatform forwards one stored platform event to the sockets that asked for
// its component.
func (r *agentSocketRegistry) pushPlatform(ev stream.PlatformEvent) {
	component := nanoprotocol.SocketComponentConfig
	switch ev.Type {
	case nanoprotocol.TypeAgentsChanged:
		component = nanoprotocol.SocketComponentAgents
	case nanoprotocol.TypeConfigChanged:
		component = nanoprotocol.SocketComponentConfig
	default:
		return
	}
	msg, err := nanoprotocol.NewMessage(nanoprotocol.TypePlatformEvent, nanoprotocol.PlatformEventNotice{
		Type:    ev.Type,
		Content: ev.Content,
	})
	if err != nil {
		return
	}
	r.mu.RLock()
	var targets []*agentSocket
	for _, conns := range r.byUser {
		for c := range conns {
			if c.wants(component) {
				targets = append(targets, c)
			}
		}
	}
	r.mu.RUnlock()
	for _, c := range targets {
		if err := c.writeJSON(msg); err != nil {
			continue
		}
		metrics.AgentSocketPushes.WithLabelValues("platform").Inc()
	}
}

// handleAgentSocketWS accepts an agent's persistent connection: register, then
// run the same message dispatch the REST endpoint runs.
func (s *Server) handleAgentSocketWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(agentSocketRegisterWait))
	var reg nanoprotocol.Message
	if err := conn.ReadJSON(&reg); err != nil {
		return
	}
	if reg.Type != nanoprotocol.TypeAgentSocketRegister {
		s.writeAgentSocketError(conn, "expected agent_socket_register")
		return
	}
	var payload nanoprotocol.AgentSocketRegister
	if err := reg.DecodePayload(&payload); err != nil || payload.UserID == "" {
		s.writeAgentSocketError(conn, "invalid agent_socket_register")
		return
	}

	sock := newAgentSocket(conn, payload)
	s.agentSockets.register(sock)
	defer s.agentSockets.unregister(sock)

	ack, _ := nanoprotocol.NewMessage(nanoprotocol.TypeAgentSocketRegistered, struct{}{})
	if err := sock.writeJSON(ack); err != nil {
		return
	}
	slog.Debug("agent socket registered", "user_id", payload.UserID, "agent_id", payload.AgentID)

	_ = conn.SetReadDeadline(time.Now().Add(agentSocketPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(agentSocketPongWait))
	})

	done := make(chan struct{})
	defer close(done)
	go keepSocketAlive(conn, agentSocketPingInterval, done)

	for {
		var msg nanoprotocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(agentSocketPongWait))

		if !sock.allow(time.Now()) {
			resp := nanoprotocol.NewErrorMessage("too many requests on the agent socket")
			if err := sock.writeJSON(resp); err != nil {
				return
			}
			continue
		}
		if resp := s.handleMessage(msg); resp != nil {
			if err := sock.writeJSON(resp); err != nil {
				return
			}
		}
	}
}

func (s *Server) writeAgentSocketError(conn *websocket.Conn, reason string) {
	resp := nanoprotocol.NewErrorMessage(reason)
	_ = writeJSONScoped(conn, hypervisorTunnelWriteTimeout, resp)
}

// keepSocketAlive pings a socket so idle connections survive intermediaries.
func keepSocketAlive(conn *websocket.Conn, interval time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(interval)
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

// consumeInboundNotices turns queue inserts into pushes for this replica's
// sockets. It is one subscription per replica, whatever the socket count.
func (s *Server) consumeInboundNotices(ctx context.Context) {
	if s.inboundTailer == nil {
		return
	}
	sub := s.inboundTailer.Subscribe("")
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case notice, ok := <-sub.Events():
			if !ok {
				return
			}
			s.agentSockets.pushInbound(notice)
		case <-sub.Lag():
			// Notices are re-derivable: a lagging subscriber only misses wake-ups,
			// and the agent's fallback poll covers them. Say so rather than
			// pretending nothing happened.
			slog.Warn("agent socket: inbound notices lagged; agents fall back to polling")
		}
	}
}

// consumePlatformNotices forwards platform events to this replica's sockets.
func (s *Server) consumePlatformNotices(ctx context.Context) {
	if s.platformTailer == nil {
		return
	}
	sub := s.platformTailer.Subscribe("")
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			s.agentSockets.pushPlatform(ev)
		case <-sub.Lag():
			slog.Warn("agent socket: platform notices lagged; agents re-read on the next event")
		}
	}
}
