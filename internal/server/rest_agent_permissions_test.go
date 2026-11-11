package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/adminclient"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// The rules live in the per-(agent, mode) override, which the admin module owns.
// Both of this service's faces — the console's REST form and the runtime's
// message — forward there, after deciding whose agent and which mode they mean.

func putPermissions(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/user/capabilities/agents/agent-1/permissions?user_id=user-1", strings.NewReader(body))
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)
	return rec
}

func permissionCalls(calls []forwardedCall) []forwardedCall {
	out := make([]forwardedCall, 0, len(calls))
	for _, c := range calls {
		if strings.HasSuffix(c.path, "/permissions") {
			out = append(out, c)
		}
	}
	return out
}

// TestRestSetAgentPermissions_ForwardsTheList: the console sends the rules it
// shows, and the list replaces what is stored.
func TestRestSetAgentPermissions_ForwardsTheList(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")
	var calls []forwardedCall
	s.admin = adminRecorder(t, &calls, http.StatusOK)

	rec := putPermissions(t, s, `{"type_id":"build","permissions":[{"pattern":"write","action":"allow"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	forwarded := permissionCalls(calls)
	if len(forwarded) != 1 {
		t.Fatalf("expected one forwarded write, got %+v", calls)
	}
	call := forwarded[0]
	if call.method != http.MethodPost || call.path != "/api/v1/internal/agents/agent-1/permissions" {
		t.Fatalf("call = %s %s", call.method, call.path)
	}
	var body struct {
		TypeID      string                        `json:"type_id"`
		Permissions []nanoprotocol.PermissionRule `json:"permissions"`
		Append      bool                          `json:"append"`
	}
	if err := json.Unmarshal([]byte(call.body), &body); err != nil {
		t.Fatalf("decode forwarded body: %v", err)
	}
	if body.TypeID != "build" || len(body.Permissions) != 1 || body.Permissions[0].Pattern != "write" {
		t.Fatalf("the list did not travel: %+v", body)
	}
	if body.Append {
		t.Fatalf("the console's form replaces; it must not be forwarded as an append: %+v", body)
	}
}

// TestRestSetAgentPermissions_FillsTheDefaultMode: the console may omit the mode,
// and the agent's own type is what it means.
func TestRestSetAgentPermissions_FillsTheDefaultMode(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")
	var calls []forwardedCall
	s.admin = adminRecorder(t, &calls, http.StatusOK)

	rec := putPermissions(t, s, `{"permissions":[]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	forwarded := permissionCalls(calls)
	if len(forwarded) != 1 || !strings.Contains(forwarded[0].body, `"type_id":"build"`) {
		t.Fatalf("the default mode was not resolved: %+v", forwarded)
	}
}

// TestRestSetAgentPermissions_ChecksTheOwnerBeforeForwarding.
func TestRestSetAgentPermissions_ChecksTheOwnerBeforeForwarding(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-2", "active")
	var calls []forwardedCall
	s.admin = adminRecorder(t, &calls, http.StatusOK)

	rec := putPermissions(t, s, `{"type_id":"build","permissions":[]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if len(permissionCalls(calls)) != 0 {
		t.Fatalf("a stranger's write was forwarded: %+v", calls)
	}
}

// TestHandleSetAgentPermission_ForwardsAnAppend: the runtime's message adds or
// replaces one rule at a time, so it must arrive marked as an append.
func TestHandleSetAgentPermission_ForwardsAnAppend(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")
	var calls []forwardedCall
	s.admin = adminRecorder(t, &calls, http.StatusOK)

	msg, err := nanoprotocol.NewMessage(nanoprotocol.TypeSetAgentPermission, nanoprotocol.SetAgentPermissionRequest{
		AgentID:     "agent-1",
		TypeID:      "build",
		Permissions: []nanoprotocol.PermissionRule{{Pattern: "write", Action: nanoprotocol.PermissionDeny}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := s.handleSetAgentPermission(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeAgentPermissionSet {
		t.Fatalf("response = %+v, want %s", resp, nanoprotocol.TypeAgentPermissionSet)
	}

	forwarded := permissionCalls(calls)
	if len(forwarded) != 1 {
		t.Fatalf("expected one forwarded append, got %+v", calls)
	}
	var body struct {
		TypeID      string                        `json:"type_id"`
		Permissions []nanoprotocol.PermissionRule `json:"permissions"`
		Append      bool                          `json:"append"`
	}
	if err := json.Unmarshal([]byte(forwarded[0].body), &body); err != nil {
		t.Fatalf("decode forwarded body: %v", err)
	}
	if !body.Append || body.TypeID != "build" || len(body.Permissions) != 1 {
		t.Fatalf("the append did not travel as one: %+v", body)
	}
}

// TestHandleSetAgentPermission_ReportsARefusedWrite: a rule the catalogue refuses
// (a read-only key relaxed) must come back as an error message, not a success.
func TestHandleSetAgentPermission_ReportsARefusedWrite(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")
	var calls []forwardedCall
	s.admin = adminRecorder(t, &calls, http.StatusBadRequest)

	msg, err := nanoprotocol.NewMessage(nanoprotocol.TypeSetAgentPermission, nanoprotocol.SetAgentPermissionRequest{
		AgentID: "agent-1", TypeID: "build",
		Permissions: []nanoprotocol.PermissionRule{{Pattern: "read", Action: nanoprotocol.PermissionDeny}},
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := s.handleSetAgentPermission(msg)
	if resp == nil || resp.Error == "" {
		t.Fatalf("response = %+v, want an error", resp)
	}
}

// TestRestSetAgentPermissions_PassesARejectionThrough: a read-only key the
// catalogue refuses is the caller's mistake, so the console must see the reason
// instead of a generic "catalogue unavailable" — the write neither happened nor
// was the module down.
func TestRestSetAgentPermissions_PassesARejectionThrough(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")
	var calls []forwardedCall
	rejection := `{"error":"permission key \"read\" is read-only and must remain \"allow\""}`
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, forwardedCall{r.Method, r.URL.Path, string(body)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(rejection))
	}))
	t.Cleanup(stub.Close)
	s.admin = adminclient.New(stub.URL, "test-token")

	rec := putPermissions(t, s, `{"type_id":"build","permissions":[{"pattern":"read","action":"deny"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "read-only") {
		t.Fatalf("the refusal lost its reason: %s", rec.Body.String())
	}
}
