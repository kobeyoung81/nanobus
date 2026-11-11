package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
)

// The starred git paths are identity's now; these three endpoints keep their shape
// and forward the call. What is asserted here is that the console's contract did
// not change and that the module's answers come back as themselves.

type authCall struct{ method, path, body, auth string }

func authStub(t *testing.T, calls *[]authCall, status int, answer string) *authclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, authCall{r.Method, r.URL.RequestURI(), string(body), r.Header.Get("Authorization")})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	return authclient.New(srv.URL, "test-token")
}

func TestRestGitShortcutsForwardAndKeepTheirShape(t *testing.T) {
	var calls []authCall
	s := wireServer(t)
	s.auth = authStub(t, &calls, http.StatusOK,
		`{"shortcuts":[{"id":7,"user_id":"u-1","agent_id":"a-1","path":"docs","label":"Docs"}]}`)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u-1/git-shortcuts?agent_id=a-1", nil)
	req.SetPathValue("userID", "u-1")
	rec := httptest.NewRecorder()
	s.restListGitShortcuts(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Shortcuts []map[string]any `json:"shortcuts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Shortcuts) != 1 || payload.Shortcuts[0]["label"] != "Docs" {
		t.Fatalf("the console's shape changed: %+v", payload.Shortcuts)
	}
	if len(calls) != 1 || calls[0].path != "/internal/v1/users/u-1/git-shortcuts?agent_id=a-1" {
		t.Fatalf("call did not travel: %+v", calls)
	}
	if calls[0].auth != "Bearer test-token" {
		t.Fatalf("the internal token did not travel: %q", calls[0].auth)
	}
}

func TestRestAddGitShortcutForwardsTheBody(t *testing.T) {
	var calls []authCall
	s := wireServer(t)
	s.auth = authStub(t, &calls, http.StatusOK,
		`{"id":9,"user_id":"u-1","agent_id":"a-1","path":"src","label":"Source"}`)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/u-1/git-shortcuts",
		strings.NewReader(`{"agent_id":"a-1","path":"src","label":"Source"}`))
	req.SetPathValue("userID", "u-1")
	rec := httptest.NewRecorder()
	s.restAddGitShortcut(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 || calls[0].method != http.MethodPost || calls[0].path != "/internal/v1/users/u-1/git-shortcuts" {
		t.Fatalf("call did not travel: %+v", calls)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode forwarded body: %v", err)
	}
	if body["agent_id"] != "a-1" || body["path"] != "src" || body["label"] != "Source" {
		t.Fatalf("the body lost a field: %+v", body)
	}
	// The answer carries the id the console deletes by.
	var answer map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if answer["id"] != float64(9) {
		t.Fatalf("the id did not come back: %+v", answer)
	}
}

// TestRestDeleteGitShortcutMapsNotFound: nanoauth answers 404 for a star that is
// gone (or somebody else's), and the console shows its own message for that.
func TestRestDeleteGitShortcutMapsNotFound(t *testing.T) {
	var calls []authCall
	s := wireServer(t)
	s.auth = authStub(t, &calls, http.StatusNotFound, `{"error":"shortcut 9 not found"}`)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/u-1/git-shortcuts/9", nil)
	req.SetPathValue("userID", "u-1")
	req.SetPathValue("id", "9")
	rec := httptest.NewRecorder()
	s.restDeleteGitShortcut(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 || calls[0].method != http.MethodDelete || calls[0].path != "/internal/v1/users/u-1/git-shortcuts/9" {
		t.Fatalf("call did not travel: %+v", calls)
	}
}

// TestRestGitShortcutsReportAnUnavailableModule: the write did not happen, so the
// answer must not look like success.
func TestRestGitShortcutsReportAnUnavailableModule(t *testing.T) {
	var calls []authCall
	s := wireServer(t)
	s.auth = authStub(t, &calls, http.StatusInternalServerError, `{"error":"boom"}`)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u-1/git-shortcuts", nil)
	req.SetPathValue("userID", "u-1")
	rec := httptest.NewRecorder()
	s.restListGitShortcuts(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}
