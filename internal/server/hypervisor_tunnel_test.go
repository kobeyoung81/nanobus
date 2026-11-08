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

func TestHypervisorTunnelFilesRoundTrip(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "running")

	busWS := httptest.NewServer(http.HandlerFunc(s.handleHypervisorTunnelWS))
	defer busWS.Close()

	registered := make(chan struct{})
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

		if err := conn.ReadJSON(&m); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if m.Type != nanoprotocol.TypeTunnelRequest {
			t.Errorf("expected tunnel_request, got %s", m.Type)
			return
		}
		var req nanoprotocol.TunnelRequest
		if err := m.DecodePayload(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.Path != "/api/v1/files/agent-1" {
			t.Errorf("unexpected request path: %q", req.Path)
		}

		resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeTunnelResponse, nanoprotocol.TunnelResponse{
			RequestID: req.RequestID,
			Status:    http.StatusOK,
			Body:      []byte(`{"agent_id":"agent-1","entries":[]}`),
		})
		_ = conn.WriteJSON(resp)
		time.Sleep(50 * time.Millisecond)
	}()

	<-registered

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/agent-1", nil)
	req.SetPathValue("agentID", "agent-1")
	s.restFileList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "agent-1") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	<-hypervisorDone
}

func TestHypervisorTunnelFileContentRoundTrip(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "running")

	busWS := httptest.NewServer(http.HandlerFunc(s.handleHypervisorTunnelWS))
	defer busWS.Close()

	registered := make(chan struct{})
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

		if err := conn.ReadJSON(&m); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var req nanoprotocol.TunnelRequest
		if err := m.DecodePayload(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", req.Method)
		}
		if req.Path != "/api/v1/files/agent-1/content?path=notes.md" {
			t.Errorf("unexpected request path: %q", req.Path)
		}
		if string(req.Body) != "hello workspace" {
			t.Errorf("unexpected request body: %q", req.Body)
		}

		resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeTunnelResponse, nanoprotocol.TunnelResponse{
			RequestID: req.RequestID,
			Status:    http.StatusOK,
			Body:      []byte(`{"status":"ok"}`),
		})
		_ = conn.WriteJSON(resp)
		time.Sleep(50 * time.Millisecond)
	}()

	<-registered

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/files/agent-1/content?path=notes.md", strings.NewReader("hello workspace"))
	req.SetPathValue("agentID", "agent-1")
	s.restFileContentPUT(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ok") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	<-hypervisorDone
}

// TestHypervisorTunnelMkdirAndRename verifies the mkdir and rename routes are
// tunneled to the owning hypervisor with the expected request paths.
func TestHypervisorTunnelMkdirAndRename(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "running")

	busWS := httptest.NewServer(http.HandlerFunc(s.handleHypervisorTunnelWS))
	defer busWS.Close()

	wantPaths := []string{
		"/api/v1/files/agent-1/mkdir?path=folder",
		"/api/v1/files/agent-1/rename?path=folder%2Fnotes.md&to=folder%2Frenamed.md",
		"/api/v1/files/agent-1/copy?path=folder%2Fnotes.md&to=folder%2Fnotes-copy.md",
	}

	registered := make(chan struct{})
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

		for _, want := range wantPaths {
			if err := conn.ReadJSON(&m); err != nil {
				t.Errorf("read request: %v", err)
				return
			}
			var req nanoprotocol.TunnelRequest
			if err := m.DecodePayload(&req); err != nil {
				t.Errorf("decode request: %v", err)
				return
			}
			if req.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", req.Method)
			}
			if req.Path != want {
				t.Errorf("unexpected request path: %q, want %q", req.Path, want)
			}
			resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeTunnelResponse, nanoprotocol.TunnelResponse{
				RequestID: req.RequestID,
				Status:    http.StatusOK,
				Body:      []byte(`{"status":"ok"}`),
			})
			_ = conn.WriteJSON(resp)
		}
		time.Sleep(50 * time.Millisecond)
	}()

	<-registered

	mkdirRec := httptest.NewRecorder()
	mkdirReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/agent-1/mkdir?path=folder", nil)
	mkdirReq.SetPathValue("agentID", "agent-1")
	s.restFileMkdir(mkdirRec, mkdirReq)
	if mkdirRec.Code != http.StatusOK {
		t.Fatalf("mkdir: expected 200, got %d: %s", mkdirRec.Code, mkdirRec.Body.String())
	}

	renameRec := httptest.NewRecorder()
	renameReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/agent-1/rename?path=folder%2Fnotes.md&to=folder%2Frenamed.md", nil)
	renameReq.SetPathValue("agentID", "agent-1")
	s.restFileRename(renameRec, renameReq)
	if renameRec.Code != http.StatusOK {
		t.Fatalf("rename: expected 200, got %d: %s", renameRec.Code, renameRec.Body.String())
	}

	copyRec := httptest.NewRecorder()
	copyReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/agent-1/copy?path=folder%2Fnotes.md&to=folder%2Fnotes-copy.md", nil)
	copyReq.SetPathValue("agentID", "agent-1")
	s.restFileCopy(copyRec, copyReq)
	if copyRec.Code != http.StatusOK {
		t.Fatalf("copy: expected 200, got %d: %s", copyRec.Code, copyRec.Body.String())
	}

	<-hypervisorDone
}

// TestRestFilesNoTunnel verifies files requests fail cleanly when no
// hypervisor tunnel is registered.
func TestRestFilesNoTunnel(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "running")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/files/agent-1", nil)
	req.SetPathValue("agentID", "agent-1")
	s.restFileList(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when no tunnel registered, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "not registered") {
		t.Fatalf("expected not-registered error, got: %s", rec.Body.String())
	}
}

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

func TestHypervisorTunnelGitRoundTrip(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "running")

	busWS := httptest.NewServer(http.HandlerFunc(s.handleHypervisorTunnelWS))
	defer busWS.Close()

	registered := make(chan struct{})
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
		_ = conn.WriteJSON(reg)
		var m nanoprotocol.Message
		_ = conn.ReadJSON(&m)
		close(registered)

		if err := conn.ReadJSON(&m); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var req nanoprotocol.TunnelRequest
		if err := m.DecodePayload(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		wantPath := "/api/v1/git/agent-1/status?path=work/proj"
		if req.Path != wantPath {
			t.Errorf("unexpected request path: %q, want %q", req.Path, wantPath)
		}
		if req.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", req.Method)
		}

		resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeTunnelResponse, nanoprotocol.TunnelResponse{
			RequestID: req.RequestID,
			Status:    http.StatusOK,
			Body:      []byte(`{"branch":"main","path":"work/proj"}`),
		})
		_ = conn.WriteJSON(resp)
		time.Sleep(50 * time.Millisecond)
	}()

	<-registered

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/git/agent-1/status?path=work/proj", nil)
	req.SetPathValue("agentID", "agent-1")
	req.SetPathValue("op", "status")
	s.restGit(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "main") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}

	<-hypervisorDone
}
