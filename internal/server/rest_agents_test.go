package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func restMux(t *testing.T, s *Server) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	return mux
}

func TestRestPollInbound_Empty(t *testing.T) {
	// The queue moved to MongoDB: an empty queue is now an empty collection
	// rather than an empty result set.
	s := testServerMongo(t)

	body := `{"user_id":"user-1","binding_id":0,"agent_id":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound/poll", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRestCreateAgent_Success(t *testing.T) {
	s := testServerMongo(t)

	// The tier, the container request and the agent are all documents now; the
	// user is seeded rather than mocked.
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)

	body := `{"user_id":"user-1","id":"devops","display_name":"DevOps Agent"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRestCreateAgent_TierLimit(t *testing.T) {
	s := testServerMongo(t)

	// free allows 5 agents; seed that many so the limit is reached.
	for _, id := range []string{"first", "second", "third", "fourth", "fifth"} {
		seedAgent(t, s, id, "user-1", "active")
	}
	storetest.SeedUser(t, s.store, "user-1", "free", 1)

	body := `{"user_id":"user-1","id":"sixth","display_name":"Sixth"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRestCreateAgent_DuplicateName(t *testing.T) {
	s := testServerMongo(t)

	// The name check reads the agent documents now, so the duplicate has to exist
	// as one rather than as a mocked row count.
	seedAgentNamed(t, s, "devops", "user-1", "active", "DevOps Agent")
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)

	body := `{"user_id":"user-1","id":"devops2","display_name":"DevOps Agent"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("already exists")) {
		t.Fatalf("expected duplicate-name error, got %s", rec.Body.String())
	}
}

func TestRestListAgents(t *testing.T) {
	s := testServerMongo(t)

	// The list reads the agent documents now, so the agent has to exist as one.
	seedAgentNamed(t, s, "assistant", "user-1", "active", "Assistant")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents?user_id=user-1", nil)
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("assistant")) {
		t.Fatalf("expected assistant in body, got %s", rec.Body.String())
	}
}

func TestRestGetConfig(t *testing.T) {
	// Read through the HTTP layer against a real store, so what is asserted is
	// the response the endpoint produces from actual data rather than the SQL
	// string a mock was told to expect.
	s := testServerMongo(t)
	if err := seedConfig(t, s.store, "agent", []store.ConfigItem{
		{Key: "agent.maxIterations", Value: "10", ValueType: "int"},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config/agent", nil)
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("agent.maxIterations")) {
		t.Fatalf("expected config key in body, got %s", rec.Body.String())
	}
}

// The user-facing agent update is the write path for the per-agent settings the
// UI owns; anything outside the whitelist must be refused, not stored.
func TestRestUpdateAgent_SettingsValidation(t *testing.T) {
	for name, body := range map[string]string{
		"scheduled_task_mode": `{"settings":{"scheduled_task_mode":"plan"}}`,
		"unknown key":         `{"settings":{"shell_timeout":"5"}}`,
		"bad mode value":      `{"settings":{"scheduled_task_mode":"turbo"}}`,
	} {
		s := testServerMongo(t)
		seedAgentNamed(t, s, "agent-1", "user-1", "active", "Research")

		want := http.StatusBadRequest
		if name == "scheduled_task_mode" {
			want = http.StatusOK
		}

		req := httptest.NewRequest(http.MethodPut, "/api/v1/agents/agent-1", bytes.NewReader([]byte(body)))
		rec := httptest.NewRecorder()
		restMux(t, s).ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, want, rec.Body.String())
		}
	}
}
