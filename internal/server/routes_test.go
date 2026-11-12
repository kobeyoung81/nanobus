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

// The portal owns the agent rows and their groups, so the management surface
// lives there. This is the guard that keeps the bus from growing a second one:
// the old paths must be unreachable and the route table must not name them.
func TestBusDoesNotServeAgentManagement(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	requests := []struct{ method, path string }{
		{http.MethodGet, "/api/v1/agents?user_id=u"},
		{http.MethodPost, "/api/v1/agents"},
		{http.MethodGet, "/api/v1/agents/attention?user_id=u"},
		{http.MethodGet, "/api/v1/agents/a1"},
		{http.MethodPut, "/api/v1/agents/a1"},
		{http.MethodDelete, "/api/v1/agents/a1"},
		{http.MethodPost, "/api/v1/agents/a1/restart"},
		{http.MethodPost, "/api/v1/agents/a1/provision"},
		{http.MethodPost, "/api/v1/agents/a1/restore"},
		{http.MethodPut, "/api/v1/agents/a1/status"},
		{http.MethodGet, "/api/v1/agent-groups?user_id=u"},
		{http.MethodPost, "/api/v1/agent-groups"},
		{http.MethodPost, "/api/v1/agent-groups/arrange"},
		{http.MethodPost, "/api/v1/agent-groups/reorder"},
		{http.MethodPut, "/api/v1/agent-groups/g1"},
		{http.MethodDelete, "/api/v1/agent-groups/g1"},
	}
	for _, req := range requests {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, strings.NewReader(`{}`)))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404: agent management is the portal's", req.method, req.path, rec.Code)
		}
	}

	source, err := os.ReadFile("handler_rest.go")
	if err != nil {
		t.Fatalf("read the route table: %v", err)
	}
	for _, banned := range []string{`"/api/v1/agents`, `"/api/v1/agent-groups`} {
		if strings.Contains(string(source), banned) {
			t.Errorf("handler_rest.go names %s again: the portal owns that surface", banned)
		}
	}
	// The two internal relays must still be there: they are how the portal's
	// writes reach the supervisors and the running processes.
	for _, want := range []string{"POST /api/v1/internal/agents-changed", "POST /api/v1/internal/agents-settings-changed"} {
		if !strings.Contains(string(source), want) {
			t.Errorf("handler_rest.go is missing %s", want)
		}
	}
}

// The accounts are nanoauth's, and it signs up through its own store: the bus
// used to expose a create endpoint that only nanoauth ever called, which made a
// registration loop back through a second module. What has to stay true is that
// the loop is gone — the route must be unreachable and the table must not name
// it — while the lookups the console and the runtime still use remain.
func TestBusDoesNotCreateAccounts(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/users",
		strings.NewReader(`{"id":"u-1","email":"a@b.c","password_hash":"h"}`)))
	// 405 rather than 404: the path still serves the list, and the method it does
	// not answer is the create.
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/v1/users = %d, want 405: nanoauth writes its own accounts", rec.Code)
	}

	source, err := os.ReadFile("handler_rest.go")
	if err != nil {
		t.Fatalf("read the route table: %v", err)
	}
	if strings.Contains(string(source), `"POST /api/v1/users"`) {
		t.Error(`handler_rest.go names "POST /api/v1/users" again: the account create is nanoauth's`)
	}
	for _, want := range []string{`"POST /api/v1/users/resolve"`, `"GET /api/v1/users"`, `"GET /api/v1/user-accounts"`} {
		if !strings.Contains(string(source), want) {
			t.Errorf("handler_rest.go is missing %s: the console and the gateway still read accounts", want)
		}
	}
}
