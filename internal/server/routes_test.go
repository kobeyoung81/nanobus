package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// The bus routes messages. Configuration belongs to the admin module, which
// serves it over HTTP; a component that needs its settings asks admin directly.
//
// This test is the guard that keeps that true. It fails two ways on purpose:
// behaviour (the old paths must be 404) and shape (the route table must not name
// them again), because a re-added route would otherwise only show up as "the bus
// still seems to own configuration" in a review.
func TestBusDoesNotServeConfiguration(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	requests := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/config"},
		{http.MethodGet, "/api/v1/config/agent"},
		{http.MethodGet, "/api/v1/config/agent/agent.language.default"},
		{http.MethodPut, "/api/v1/config/agent/agent.language.default"},
		{http.MethodGet, "/api/v1/config-updated-at/agent"},
		{http.MethodGet, "/api/v1/config-counts/agent"},
	}
	for _, req := range requests {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, strings.NewReader(`{"value":"x"}`)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404: the bus must not serve configuration", req.method, req.path, rec.Code)
		}
	}

	// The voice endpoint is not a configuration surface: it is a user-facing
	// feature that happens to read the voice component from admin.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/user/voice/config?user_id=u", nil))
	if rec.Code == http.StatusNotFound {
		t.Error("the voice user endpoint disappeared with the config surface")
	}

	source, err := os.ReadFile("handler_rest.go")
	if err != nil {
		t.Fatalf("read the route table: %v", err)
	}
	if strings.Contains(string(source), `"/api/v1/config`) {
		t.Error("handler_rest.go names a /api/v1/config route again: configuration is the admin module's")
	}
}
