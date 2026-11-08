package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestProxyWebSocket_Echo verifies bidirectional frame relay between a client
// and upstream WebSocket over the tunnel proxy.
func TestProxyWebSocket_Echo(t *testing.T) {
	// Upstream echo server.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Upgrade(w, r, nil, 1024, 1024)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.WriteMessage(mt, data); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()

	upstreamWS := "ws" + upstream.URL[len("http"):]

	reg := NewTunnelRegistry()
	reg.Register("echo", upstreamWS)

	// Create a bus server so handleTunnelWS has a tunnels registry.
	srv := NewServer(nil)
	srv.tunnels = reg

	// Serve the tunnel handler.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws/tunnel/{name}", srv.handleTunnelWS)
	busSrv := httptest.NewServer(mux)
	defer busSrv.Close()

	clientWS := "ws" + busSrv.URL[len("http"):] + "/ws/tunnel/echo"
	conn, _, err := websocket.DefaultDialer.Dial(clientWS, nil)
	if err != nil {
		t.Fatalf("dial tunnel: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "ping" {
		t.Fatalf("echo = %q, want ping", string(data))
	}
}

func TestTunnelRegistry_UnknownTunnel(t *testing.T) {
	reg := NewTunnelRegistry()
	if _, ok := reg.Lookup("nope"); ok {
		t.Fatal("expected unknown tunnel lookup to fail")
	}

	reg.Register("a", "ws://localhost:1")
	if url, ok := reg.Lookup("a"); !ok || url != "ws://localhost:1" {
		t.Fatalf("lookup a = %q, %v", url, ok)
	}

	reg.Unregister("a")
	if _, ok := reg.Lookup("a"); ok {
		t.Fatal("expected tunnel removed after unregister")
	}
}
