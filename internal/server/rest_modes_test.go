package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/adminclient"
)

// forwardedCall is one request the bus handed to the admin module.
type forwardedCall struct {
	method string
	path   string
	body   string
}

// adminRecorder stands in for the admin module: it records what the bus forwarded
// and answers with the status the test wants.
func adminRecorder(t *testing.T, calls *[]forwardedCall, status int) *adminclient.Client {
	t.Helper()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, forwardedCall{r.Method, r.URL.Path, string(body)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"status":"updated"}`))
	}))
	t.Cleanup(stub.Close)
	return adminclient.New(stub.URL, "test-token")
}

// modeCalls narrows a recorder's calls to the mode endpoint. The audit the same
// request appends is a second call to the same module, and a different subject.
func modeCalls(calls []forwardedCall) []forwardedCall {
	out := make([]forwardedCall, 0, len(calls))
	for _, c := range calls {
		if strings.Contains(c.path, "/modes/") {
			out = append(out, c)
		}
	}
	return out
}

// putMode writes one (agent, mode) override through the user-facing endpoint.
func putMode(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/user/agents/agent-1/modes/build?user_id=user-1", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)
	return rec
}

// TestRestSetAgentMode_ForwardsThePatch: the override document is the admin
// module's, so the console's save leaves this service as a patch — and nothing is
// written here.
func TestRestSetAgentMode_ForwardsThePatch(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")
	var calls []forwardedCall
	s.admin = adminRecorder(t, &calls, http.StatusOK)

	rec := putMode(t, s, `{"thinking":"high","clear_model_id":true,"settings":{"shell_timeout":"60s"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	forwarded := modeCalls(calls)
	if len(forwarded) != 1 {
		t.Fatalf("expected one forwarded mode write, got %+v", calls)
	}
	call := forwarded[0]
	if call.method != http.MethodPut || call.path != "/api/v1/internal/agents/agent-1/modes/build" {
		t.Fatalf("call = %s %s", call.method, call.path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(call.body), &body); err != nil {
		t.Fatalf("decode forwarded body: %v", err)
	}
	if body["thinking"] != "high" || body["clear_model_id"] != true {
		t.Fatalf("the patch lost a field: %+v", body)
	}
	settings, _ := body["settings"].(map[string]any)
	if settings["shell_timeout"] != "60s" {
		t.Fatalf("the settings did not travel: %+v", body)
	}
	if _, ok := body["model_id"]; !ok {
		t.Fatalf("model_id must be sent (null = unchanged): %+v", body)
	}

	// The bus no longer writes the document itself.
	if ov, err := s.store.GetAgentTypeOverride("agent-1", "build"); err != nil || ov != nil {
		t.Fatalf("the bus wrote the override locally: %+v (err %v)", ov, err)
	}
}

// TestRestSetAgentMode_RefusesAnUnknownThinkingLevel: the runtime would forward
// an unrecognized value to the provider as a stray reasoning_effort, so it is
// refused at the door — before anything is forwarded.
func TestRestSetAgentMode_RefusesAnUnknownThinkingLevel(t *testing.T) {
	for _, value := range []string{"banana", "minimal", "HIGH", "offf"} {
		s := testServerMongo(t)
		seedAgent(t, s, "agent-1", "user-1", "active")
		var calls []forwardedCall
		s.admin = adminRecorder(t, &calls, http.StatusOK)

		rec := putMode(t, s, `{"thinking":"`+value+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", value, rec.Code, rec.Body.String())
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "unsupported thinking level" {
			t.Errorf("%s: body = %s, want an unsupported-thinking-level error", value, rec.Body.String())
		}
		if len(modeCalls(calls)) != 0 {
			t.Errorf("%s: the refused value was forwarded anyway: %+v", value, calls)
		}
	}
}

// TestRestSetAgentMode_ChecksTheOwnerBeforeForwarding: ownership is this
// service's check — the admin module knows nothing about who owns an agent — so a
// stranger's request must never reach it.
func TestRestSetAgentMode_ChecksTheOwnerBeforeForwarding(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-2", "active")
	var calls []forwardedCall
	s.admin = adminRecorder(t, &calls, http.StatusOK)

	rec := putMode(t, s, `{"thinking":"high"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if len(modeCalls(calls)) != 0 {
		t.Fatalf("a stranger's write was forwarded: %+v", calls)
	}
}

// TestRestSetAgentMode_ReportsAnUnavailableCatalogue: the write did not happen,
// so the answer must not be a success.
func TestRestSetAgentMode_ReportsAnUnavailableCatalogue(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")
	var calls []forwardedCall
	s.admin = adminRecorder(t, &calls, http.StatusInternalServerError)

	rec := putMode(t, s, `{"thinking":"high"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", rec.Code, rec.Body.String())
	}
}
