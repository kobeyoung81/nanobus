package server

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanoprotocol"
	"github.com/gorilla/websocket"
)

// dialHypervisor registers a fake per-user hypervisor tunnel. It auto-replies
// to tunnel_request frames with a 200 and pushes every other bus->hypervisor
// frame (e.g. tunnel_stream_* control frames) to the given channel.
func dialHypervisor(t *testing.T, s *Server, userID string, frames chan nanoprotocol.Message) {
	t.Helper()
	busWS := httptest.NewServer(http.HandlerFunc(s.handleHypervisorTunnelWS))
	wsURL := "ws" + strings.TrimPrefix(busWS.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("hypervisor dial: %v", err)
	}
	reg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeTunnelRegister, nanoprotocol.TunnelRegister{UserID: userID})
	if err := conn.WriteJSON(reg); err != nil {
		t.Fatalf("write register: %v", err)
	}
	var m nanoprotocol.Message
	if err := conn.ReadJSON(&m); err != nil { // tunnel_registered ack
		t.Fatalf("read registered: %v", err)
	}
	go func() {
		defer conn.Close()
		for {
			var msg nanoprotocol.Message
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			if msg.Type == nanoprotocol.TypeTunnelRequest {
				var req nanoprotocol.TunnelRequest
				if err := msg.DecodePayload(&req); err != nil {
					continue
				}
				resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeTunnelResponse, nanoprotocol.TunnelResponse{
					RequestID: req.RequestID,
					Status:    http.StatusOK,
					Body:      []byte(`{"terminals":[],"status":"ok"}`),
				})
				_ = conn.WriteJSON(resp)
				continue
			}
			if frames != nil {
				select {
				case frames <- msg:
				default:
				}
			}
		}
	}()
	t.Cleanup(busWS.Close)
}

func waitSubscribed(t *testing.T, s *Server, terminalID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.terminalSubs.list(terminalID)) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("bind for %s not registered", terminalID)
}

func TestTerminalBridgeOwnershipDenied(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")
	bridgeWS := httptest.NewServer(http.HandlerFunc(s.handleTerminalBridgeWS))
	defer bridgeWS.Close()

	wsURL := "ws" + strings.TrimPrefix(bridgeWS.URL, "http") + "?user_id=user-2&agent_id=agent-1"
	_, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err == nil {
		t.Fatal("expected dial to fail for foreign agent")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %v", resp)
	}
}

func TestTerminalBridgeRelaysFrames(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")

	hypervisorFrames := make(chan nanoprotocol.Message, 16)
	dialHypervisor(t, s, "user-1", hypervisorFrames)

	bridgeWS := httptest.NewServer(http.HandlerFunc(s.handleTerminalBridgeWS))
	defer bridgeWS.Close()

	wsURL := "ws" + strings.TrimPrefix(bridgeWS.URL, "http") + "?user_id=user-1&agent_id=agent-1"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("bridge dial: %v", err)
	}
	defer conn.Close()

	// bind + input + resize + close
	_ = conn.WriteJSON(terminalFrame{Type: "bind", TerminalID: "t1"})
	_ = conn.WriteJSON(terminalFrame{Type: "input", TerminalID: "t1", Data: base64.StdEncoding.EncodeToString([]byte("ls -la\r"))})
	_ = conn.WriteJSON(terminalFrame{Type: "resize", TerminalID: "t1", Cols: 160, Rows: 50})
	_ = conn.WriteJSON(terminalFrame{Type: "close", TerminalID: "t1"})

	want := []struct {
		msgType string
		check   func(nanoprotocol.Message) bool
	}{
		{nanoprotocol.TypeTunnelStreamInput, func(m nanoprotocol.Message) bool {
			var p nanoprotocol.TunnelStreamInput
			if err := m.DecodePayload(&p); err != nil {
				return false
			}
			return p.StreamID == "t1" && string(p.Data) == "ls -la\r"
		}},
		{nanoprotocol.TypeTunnelStreamResize, func(m nanoprotocol.Message) bool {
			var p nanoprotocol.TunnelStreamResize
			if err := m.DecodePayload(&p); err != nil {
				return false
			}
			return p.StreamID == "t1" && p.Cols == 160 && p.Rows == 50
		}},
		{nanoprotocol.TypeTunnelStreamClose, func(m nanoprotocol.Message) bool {
			var p nanoprotocol.TunnelStreamClose
			if err := m.DecodePayload(&p); err != nil {
				return false
			}
			return p.StreamID == "t1"
		}},
	}
	for _, w := range want {
		msg := <-hypervisorFrames
		if msg.Type != w.msgType {
			t.Fatalf("expected %s frame, got %s", w.msgType, msg.Type)
		}
		if !w.check(msg) {
			t.Fatalf("unexpected payload for %s: %v", w.msgType, msg)
		}
	}
}

func TestTerminalBridgeRejectsBadResize(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")

	hypervisorFrames := make(chan nanoprotocol.Message, 16)
	dialHypervisor(t, s, "user-1", hypervisorFrames)

	bridgeWS := httptest.NewServer(http.HandlerFunc(s.handleTerminalBridgeWS))
	defer bridgeWS.Close()

	wsURL := "ws" + strings.TrimPrefix(bridgeWS.URL, "http") + "?user_id=user-1&agent_id=agent-1"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("bridge dial: %v", err)
	}
	defer conn.Close()

	// Out-of-bounds resizes must be dropped; only the legal frame is forwarded.
	for _, f := range []terminalFrame{
		{Type: "resize", TerminalID: "t1", Cols: 0, Rows: 0},
		{Type: "resize", TerminalID: "t1", Cols: 1, Rows: 1},
		{Type: "resize", TerminalID: "t1", Cols: 2000, Rows: 2000},
	} {
		if err := conn.WriteJSON(f); err != nil {
			t.Fatalf("write resize: %v", err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case msg := <-hypervisorFrames:
		t.Fatalf("out-of-bounds resize was forwarded: %s", msg.Type)
	default:
	}

	if err := conn.WriteJSON(terminalFrame{Type: "resize", TerminalID: "t1", Cols: 160, Rows: 50}); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	select {
	case msg := <-hypervisorFrames:
		if msg.Type != nanoprotocol.TypeTunnelStreamResize {
			t.Fatalf("expected resize frame, got %s", msg.Type)
		}
		var p nanoprotocol.TunnelStreamResize
		if err := msg.DecodePayload(&p); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if p.Cols != 160 || p.Rows != 50 {
			t.Fatalf("payload = %d x %d, want 160 x 50", p.Cols, p.Rows)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("valid resize frame not forwarded")
	}
	time.Sleep(50 * time.Millisecond)
	select {
	case msg := <-hypervisorFrames:
		t.Fatalf("unexpected extra frame: %s", msg.Type)
	default:
	}
}

func TestTerminalOutputRoutedToBoundBridge(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")
	dialHypervisor(t, s, "user-1", make(chan nanoprotocol.Message, 8))

	bridgeWS := httptest.NewServer(http.HandlerFunc(s.handleTerminalBridgeWS))
	defer bridgeWS.Close()

	wsURL := "ws" + strings.TrimPrefix(bridgeWS.URL, "http") + "?user_id=user-1&agent_id=agent-1"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("bridge dial: %v", err)
	}
	defer conn.Close()

	_ = conn.WriteJSON(terminalFrame{Type: "bind", TerminalID: "t1"})
	waitSubscribed(t, s, "t1")

	// Simulate the hypervisor pushing output for the bound terminal.
	s.routeTerminalOutput("t1", []byte("hello tty"))

	var frame terminalFrame
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read output frame: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil {
		t.Fatalf("decode output data: %v", err)
	}
	if frame.Type != "output" || frame.TerminalID != "t1" || string(decoded) != "hello tty" {
		t.Fatalf("unexpected frame: %+v", frame)
	}

	// Closing the session routes closed + clears the subscription.
	s.routeTerminalClosed("t1", "exit")
	if err := conn.ReadJSON(&frame); err != nil {
		t.Fatalf("read closed frame: %v", err)
	}
	if frame.Type != "closed" || frame.TerminalID != "t1" || frame.Reason != "exit" {
		t.Fatalf("unexpected closed frame: %+v", frame)
	}
}

func TestTerminalRestRoutes(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")

	dialHypervisor(t, s, "user-1", nil)

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/agents/agent-1/terminals", nil)
	listReq.SetPathValue("agentID", "agent-1")
	s.restTerminalList(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", listRec.Code, listRec.Body.String())
	}

	killRec := httptest.NewRecorder()
	killReq := httptest.NewRequest(http.MethodDelete, "/api/v1/agents/agent-1/terminals/t1", nil)
	killReq.SetPathValue("agentID", "agent-1")
	killReq.SetPathValue("terminalID", "t1")
	s.restTerminalKill(killRec, killReq)
	if killRec.Code != http.StatusOK {
		t.Fatalf("kill: expected 200, got %d: %s", killRec.Code, killRec.Body.String())
	}

}
