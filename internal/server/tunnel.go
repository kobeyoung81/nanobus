package server

import (
	"log/slog"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// TunnelRegistry maps named tunnels to upstream WebSocket URLs. The bus
// reverse-proxies connections on /ws/tunnel/{name} to the registered upstream,
// enabling streaming access (e.g., container shells) through the bus.
type TunnelRegistry struct {
	mu      sync.RWMutex
	tunnels map[string]string
}

// NewTunnelRegistry creates an empty tunnel registry.
func NewTunnelRegistry() *TunnelRegistry {
	return &TunnelRegistry{tunnels: make(map[string]string)}
}

// Register maps a tunnel name to an upstream WebSocket URL.
func (r *TunnelRegistry) Register(name, upstreamURL string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tunnels[name] = upstreamURL
}

// Unregister removes a tunnel.
func (r *TunnelRegistry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.tunnels, name)
}

// Lookup returns the upstream URL for a tunnel name.
func (r *TunnelRegistry) Lookup(name string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	url, ok := r.tunnels[name]
	return url, ok
}

var tunnelUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// handleTunnelWS reverse-proxies a client WebSocket to the tunnel's upstream.
func (s *Server) handleTunnelWS(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	upstreamURL, ok := s.tunnels.Lookup(name)
	if !ok {
		http.Error(w, "unknown tunnel", http.StatusNotFound)
		return
	}

	upstream, resp, err := websocket.DefaultDialer.Dial(upstreamURL, nil)
	if err != nil {
		slog.Warn("tunnel upstream dial failed", "tunnel", name, "error", err)
		if resp != nil {
			http.Error(w, "upstream dial failed", resp.StatusCode)
		} else {
			http.Error(w, "upstream dial failed", http.StatusBadGateway)
		}
		return
	}
	defer upstream.Close()

	client, err := tunnelUpgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("tunnel upgrade failed", "tunnel", name, "error", err)
		return
	}
	defer client.Close()

	slog.Info("tunnel connected", "tunnel", name)
	proxyWebSocket(client, upstream)
}

// proxyWebSocket bidirectionally copies frames between two WebSocket connections.
func proxyWebSocket(a, b *websocket.Conn) {
	errCh := make(chan error, 2)

	relay := func(dst, src *websocket.Conn) {
		for {
			messageType, data, err := src.ReadMessage()
			if err != nil {
				errCh <- err
				return
			}
			if err := dst.WriteMessage(messageType, data); err != nil {
				errCh <- err
				return
			}
		}
	}

	go relay(a, b)
	go relay(b, a)

	<-errCh
	_ = a.Close()
	_ = b.Close()
}
