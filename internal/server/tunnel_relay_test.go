package server

// Tunnels terminate at the provisioner now. During the dual-track window a
// container may still dial the bus (its image predates the move), so the bus
// serves what it holds locally and relays the rest. These tests pin the relay:
// a local miss must reach the provisioner, and its 404 must not be reported as
// success.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
	"gitee.com/kobeyoung81/nanoprotocol"
	"github.com/gorilla/websocket"
)

func TestForwardTunnelRequest_RelaysToTheProvisioner(t *testing.T) {
	var gotName string
	var gotReq nanoprotocol.TunnelRequest
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotName = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/internal/tunnels/"), "/request")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode forwarded request: %v", err)
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("the forward did not carry the internal token: %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(nanoprotocol.TunnelResponse{
			RequestID: gotReq.RequestID,
			Status:    http.StatusOK,
			Body:      []byte(`{"relayed":true}`),
		})
	}))
	defer stub.Close()

	s := &Server{
		hypervisorTunnels: newHypervisorTunnelRegistry(),
		provisioner:       provisionerclient.New(stub.URL, "test-token"),
	}

	resp, err := s.forwardTunnelRequest("hypervisor-user-1", nanoprotocol.TunnelRequest{
		RequestID: "req-1",
		Method:    http.MethodGet,
		Path:      "/api/v1/files/agent-1",
	}, time.Second)
	if err != nil {
		t.Fatalf("forwardTunnelRequest: %v", err)
	}
	if gotName != "hypervisor-user-1" || gotReq.Path != "/api/v1/files/agent-1" {
		t.Fatalf("the request did not reach the provisioner intact: name=%q req=%+v", gotName, gotReq)
	}
	if resp.Status != http.StatusOK || string(resp.Body) != `{"relayed":true}` {
		t.Fatalf("the relayed answer was not returned: %+v", resp)
	}
}

// TestForwardTunnelRequest_Relay404IsAnError keeps "the container is not
// connected" from looking like a successful empty response.
func TestForwardTunnelRequest_Relay404IsAnError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"tunnel is offline"}`))
	}))
	defer stub.Close()

	s := &Server{
		hypervisorTunnels: newHypervisorTunnelRegistry(),
		provisioner:       provisionerclient.New(stub.URL, "test-token"),
	}

	if _, err := s.forwardTunnelRequest("hypervisor-nobody", nanoprotocol.TunnelRequest{
		RequestID: "req-1", Method: http.MethodGet, Path: "/x",
	}, time.Second); err == nil {
		t.Fatal("a 404 from the provisioner was treated as a successful forward")
	}
}

// TestForwardTunnelRequest_PrefersTheLocalTunnel pins the other half of the
// dual track: while a container still dials the bus, its request must be served
// here and never relayed.
func TestForwardTunnelRequest_PrefersTheLocalTunnel(t *testing.T) {
	relayed := false
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		relayed = true
		_, _ = w.Write([]byte(`{}`))
	}))
	defer stub.Close()

	s := &Server{
		hypervisorTunnels: newHypervisorTunnelRegistry(),
		provisioner:       provisionerclient.New(stub.URL, "test-token"),
	}
	dialHypervisor(t, s, "user-1", nil)

	resp, err := s.forwardTunnelRequest("hypervisor-user-1", nanoprotocol.TunnelRequest{
		RequestID: "req-1", Method: http.MethodGet, Path: "/x",
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("forwardTunnelRequest: %v", err)
	}
	if resp.Status != http.StatusOK {
		t.Fatalf("the local tunnel did not answer: %+v", resp)
	}
	if relayed {
		t.Fatal("a locally registered tunnel was relayed to the provisioner")
	}
}

// TestNamedTunnelsAreGone: the ad-hoc "register an upstream WebSocket and I will
// proxy to it" surface had no callers left, and it duplicated what the hypervisor
// tunnel does with a protocol instead of a URL. The guard fails both on behaviour
// and on the route table so it cannot quietly come back.
func TestNamedTunnelsAreGone(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	cases := []struct{ method, path string }{
		{http.MethodPost, "/api/v1/tunnels"},
		{http.MethodDelete, "/api/v1/tunnels/x"},
		{http.MethodGet, "/ws/tunnel/x"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"name":"x","upstream_url":"ws://y"}`)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}

	source, err := os.ReadFile("handler_rest.go")
	if err != nil {
		t.Fatalf("read the route table: %v", err)
	}
	if strings.Contains(string(source), "/ws/tunnel/{name}") || strings.Contains(string(source), "restRegisterTunnel") {
		t.Fatal("the named-tunnel routes are back in handler_rest.go")
	}
}

// TestRelayEndpoint_MapsTheTwoFailures pins the HTTP surface the workspace
// module calls: 404 means "not connected here" (it then asks the next hop), 502
// means the tunnel exists but the forward failed. A flattened answer would make
// the caller retry a dead container forever or give up on a live one.
func TestRelayEndpoint_MapsTheTwoFailures(t *testing.T) {
	s := &Server{hypervisorTunnels: newHypervisorTunnelRegistry()}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/internal/tunnels/{name}/request", s.restForwardTunnelRequest)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body := `{"method":"GET","path":"/api/v1/files/agent-1"}`
	post := func(name string) *http.Response {
		resp, err := http.Post(srv.URL+"/api/v1/internal/tunnels/"+name+"/request", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	if got := post("hypervisor-nobody").StatusCode; got != http.StatusNotFound {
		t.Fatalf("unknown tunnel: status = %d, want 404", got)
	}

	// A registered tunnel whose write fails is a 502, not "offline". The fake
	// container accepts the handshake and disappears, so the write errors the
	// way a half-dead connection does.
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			_ = c.Close()
		}
	}))
	defer gone.Close()
	wsURL := "ws" + strings.TrimPrefix(gone.URL, "http")
	dead, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial the fake container: %v", err)
	}
	_ = dead.Close()
	conn := &hypervisorConn{conn: dead, pending: map[string]chan *nanoprotocol.TunnelResponse{}}
	s.hypervisorTunnels.register("hypervisor-user-1", conn)
	if got := post("hypervisor-user-1").StatusCode; got != http.StatusBadGateway {
		t.Fatalf("broken tunnel: status = %d, want 502", got)
	}
}

// TestFileAndGitRoutesAreGone: the workspace module serves them now, through the
// tunnel; a route here would be a second implementation of the same URL and a
// way for older callers to bypass the workspace's ownership check.
func TestFileAndGitRoutesAreGone(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/files/agent-1"},
		{http.MethodGet, "/api/v1/files/agent-1/content"},
		{http.MethodPut, "/api/v1/files/agent-1/content"},
		{http.MethodPost, "/api/v1/files/agent-1/mkdir"},
		{http.MethodGet, "/api/v1/git/agent-1/status"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}
