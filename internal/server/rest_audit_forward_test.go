package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/adminclient"
)

// auditStub stands in for the admin module: it records what the bus sent and
// answers with the payload a real one would.
func auditStub(t *testing.T, calls *[]struct{ method, path, body, auth string }, answer string, status int) *adminclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, struct{ method, path, body, auth string }{
			r.Method, r.URL.String(), string(body), r.Header.Get("Authorization"),
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	return adminclient.New(srv.URL, "test-token")
}

// The trail is the admin module's collection. What this pins is that the bus
// reports its own handlers' actions there — same path, same token, the caller's
// identity in the body — instead of writing rows itself.
func TestRecordAuditPostsToTheAdminModule(t *testing.T) {
	var calls []struct{ method, path, body, auth string }
	s := &Server{admin: auditStub(t, &calls, `{"status":"recorded"}`, http.StatusCreated)}

	r := httptest.NewRequest(http.MethodPut, "/api/v1/config/agent/x", nil)
	r.Header.Set("X-Admin-User", "u-1")
	r.Header.Set("X-Admin-Role", "admin")
	r.Header.Set("X-Forwarded-For", "10.1.2.3:5555")
	s.recordAudit(r, "config.update", "config", "agent/x", map[string]any{"key": "x"})

	if len(calls) != 1 {
		t.Fatalf("expected one call to the admin module, got %d", len(calls))
	}
	call := calls[0]
	if call.method != http.MethodPost || call.path != "/api/v1/internal/audit" {
		t.Fatalf("call = %s %s, want POST /api/v1/internal/audit", call.method, call.path)
	}
	if call.auth != "Bearer test-token" {
		t.Fatalf("the internal token did not travel: %q", call.auth)
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(call.body), &entry); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if entry["actor_id"] != "u-1" || entry["actor_role"] != "admin" ||
		entry["action"] != "config.update" || entry["resource_type"] != "config" ||
		entry["resource_id"] != "agent/x" || entry["ip"] != "10.1.2.3" {
		t.Fatalf("entry lost a field: %+v", entry)
	}
	if _, ok := entry["id"]; ok {
		t.Fatalf("the entry was sent with an id: %+v", entry)
	}
}

// An unwritable trail must not fail the operation it describes: recordAudit has
// no error to return, and the handler carries on. A failing admin module is the
// case a deployment actually hits.
func TestRecordAuditSurvivesAnAdminFailure(t *testing.T) {
	var calls []struct{ method, path, body, auth string }
	// A stub that answers 500 requires a client built by hand; auditStub returns
	// a healthy one, so build the failing case directly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, struct{ method, path, body, auth string }{r.Method, r.URL.Path, "", ""})
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := &Server{admin: adminclient.New(srv.URL, "test-token")}

	s.recordAudit(httptest.NewRequest(http.MethodPost, "/api/v1/agents", nil), "agent.create", "agent", "a1", nil)
	if len(calls) != 1 {
		t.Fatalf("the bus did not even try to record the entry: %d calls", len(calls))
	}

	// And with no admin module configured at all, the call is skipped rather
	// than turning into a nil dereference.
	(&Server{}).recordAudit(httptest.NewRequest(http.MethodPost, "/api/v1/agents", nil), "agent.create", "agent", "a1", nil)
}

// The console's read still works through the bus, forwarded verbatim: the route
// is kept so a caller that addresses the bus is not broken by the move.
func TestRestListAuditForwardsToTheAdminModule(t *testing.T) {
	var calls []struct{ method, path, body, auth string }
	s := &Server{admin: auditStub(t, &calls, `{"entries":[{"id":7,"action":"agent.delete"}]}`, http.StatusOK)}

	rec := httptest.NewRecorder()
	s.restListAudit(rec, httptest.NewRequest(http.MethodGet, "/api/v1/audit?limit=10&action=agent.delete", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 || calls[0].path != "/api/v1/audit?limit=10&action=agent.delete" {
		t.Fatalf("the conditions did not travel: %+v", calls)
	}
	if got := rec.Body.String(); got != `{"entries":[{"id":7,"action":"agent.delete"}]}` {
		t.Fatalf("the answer was not passed through: %s", got)
	}
}
