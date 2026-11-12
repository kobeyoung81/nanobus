package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
	"github.com/gorilla/websocket"
)

// TestRestFilesNoTunnel verifies files requests fail cleanly when no
// hypervisor tunnel is registered.
// TestForwardTunnelRequestWriteBounded verifies the bus write to a hypervisor
// is bounded: when the hypervisor stops reading, forwardTunnelRequest returns
// an error instead of blocking forever (which previously made workspace
// uploads/downloads hang until the portal-side timeout).
func TestForwardTunnelRequestWriteBounded(t *testing.T) {
	s := NewServer(storetest.Connect(t))

	busWS := httptest.NewServer(http.HandlerFunc(s.handleHypervisorTunnelWS))
	defer busWS.Close()

	registered := make(chan struct{})
	hypervisorStop := make(chan struct{})
	hypervisorDone := make(chan struct{})
	go func() {
		defer close(hypervisorDone)
		wsURL := "ws" + strings.TrimPrefix(busWS.URL, "http")
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Errorf("hypervisor dial: %v", err)
			return
		}
		defer conn.Close()

		reg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeTunnelRegister, nanoprotocol.TunnelRegister{UserID: "user-1"})
		if err := conn.WriteJSON(reg); err != nil {
			t.Errorf("write register: %v", err)
			return
		}
		var m nanoprotocol.Message
		if err := conn.ReadJSON(&m); err != nil {
			t.Errorf("read registered: %v", err)
			return
		}
		close(registered)
		// Stop reading after registration, simulating a stalled tunnel.
		<-hypervisorStop
	}()
	<-registered

	old := hypervisorTunnelWriteTimeout
	hypervisorTunnelWriteTimeout = 300 * time.Millisecond
	defer func() { hypervisorTunnelWriteTimeout = old }()

	// A request body large enough to exceed socket buffers forces the write to
	// block against the stalled peer.
	req := nanoprotocol.TunnelRequest{
		RequestID: "r1",
		Method:    http.MethodPut,
		Path:      "/api/v1/files/agent-1/content?path=big.bin",
		Body:      []byte(strings.Repeat("x", 8<<20)),
	}
	start := time.Now()
	_, err := s.forwardTunnelRequest("hypervisor-user-1", req, 5*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected forwardTunnelRequest to fail against a stalled peer")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("forwardTunnelRequest was not bounded; took %v", elapsed)
	}

	close(hypervisorStop)
	<-hypervisorDone
}
