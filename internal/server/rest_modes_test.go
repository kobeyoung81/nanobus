package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// putMode writes one (agent, mode) override through the user-facing endpoint.
func putMode(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/user/agents/agent-1/modes/build?user_id=user-1", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)
	return rec
}

// storedThinking reads the override back from the store the server writes to.
func storedThinking(t *testing.T, s *Server) string {
	t.Helper()
	ov, err := s.store.GetAgentTypeOverride("agent-1", "build")
	if err != nil {
		t.Fatalf("read override: %v", err)
	}
	if ov == nil {
		return ""
	}
	return ov.Thinking
}

// TestRestSetAgentMode_StoresTheThinkingLevel: the mode write is the only path
// that sets a thinking level, so the stored value is what the runtime resolves.
func TestRestSetAgentMode_StoresTheThinkingLevel(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")

	rec := putMode(t, s, `{"thinking":"high"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := storedThinking(t, s); got != "high" {
		t.Fatalf("stored thinking = %q, want high", got)
	}

	// "" is how a client asks for "follow the platform default" again.
	rec = putMode(t, s, `{"thinking":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if got := storedThinking(t, s); got != "" {
		t.Fatalf("stored thinking = %q, want empty after a clear", got)
	}
}

// TestRestSetAgentMode_RefusesAnUnknownThinkingLevel: the runtime would forward
// an unrecognized value to the provider as a stray reasoning_effort, so it is
// refused at the door instead of stored.
func TestRestSetAgentMode_RefusesAnUnknownThinkingLevel(t *testing.T) {
	for _, value := range []string{"banana", "minimal", "HIGH", "offf"} {
		s := testServerMongo(t)
		seedAgent(t, s, "agent-1", "user-1", "active")

		rec := putMode(t, s, `{"thinking":"`+value+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", value, rec.Code, rec.Body.String())
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] != "unsupported thinking level" {
			t.Errorf("%s: body = %s, want an unsupported-thinking-level error", value, rec.Body.String())
		}
		if got := storedThinking(t, s); got != "" {
			t.Errorf("%s: stored thinking = %q, want nothing stored", value, got)
		}
	}
}
