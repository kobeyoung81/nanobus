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

// The accounts are nanoauth's, end to end: it signs up through its own store, and
// the console now asks it directly for the list, the counts and the password. What
// has to stay true is that the bus neither creates nor serves accounts — only the
// identity resolution the gateway needs (`/api/v1/users/resolve`) remains.
func TestBusDoesNotCreateAccounts(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/users",
		strings.NewReader(`{"id":"u-1","email":"a@b.c","password_hash":"h"}`)))
	// The path is gone entirely: nanoauth creates accounts through its own store.
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /api/v1/users = %d, want 404: nanoauth writes its own accounts", rec.Code)
	}

	source, err := os.ReadFile("handler_rest.go")
	if err != nil {
		t.Fatalf("read the route table: %v", err)
	}
	if strings.Contains(string(source), `"POST /api/v1/users"`) {
		t.Error(`handler_rest.go names "POST /api/v1/users" again: the account create is nanoauth's`)
	}
	if !strings.Contains(string(source), `"POST /api/v1/users/resolve"`) {
		t.Error("handler_rest.go is missing POST /api/v1/users/resolve: the gateway resolves identities here")
	}
	// The account surface moved to nanoauth with the row: a forwarding route here
	// is the two-hop we just removed.
	for _, gone := range []string{`/api/v1/user-accounts`, `git-shortcuts`, `oauth-links`, `user/preferences`} {
		if strings.Contains(string(source), gone) {
			t.Errorf("handler_rest.go still serves %s: nanoauth owns it now", gone)
		}
	}
}

// TestBusDoesNotDriveTheContainerLifecycle: restart, dispose and reprovision are
// the admin module's control plane now. The bus keeps the paths the platform
// itself uses (an agent's container request, the runner registry) and nothing
// else, so a page that still points at the old path fails loudly here instead of
// half-working.
func TestBusDoesNotDriveTheContainerLifecycle(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	for _, action := range []string{"restart", "dispose", "reprovision"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/containers/u-1/"+action, nil))
		// The route table alone answers 404; the full handler's WebSocket
		// catch-all turns "no such path" into 400.
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST /api/v1/containers/u-1/%s = %d, want 404 (no such route): the admin module owns it",
				action, rec.Code)
		}
	}

	source, err := os.ReadFile("handler_rest.go")
	if err != nil {
		t.Fatalf("read the route table: %v", err)
	}
	if strings.Contains(string(source), "/containers/{id}/restart") {
		t.Error("handler_rest.go registers the container lifecycle again: admin serves it")
	}
	for _, want := range []string{`"POST /api/v1/containers"`, `"GET /api/v1/containers"`} {
		if !strings.Contains(string(source), want) {
			t.Errorf("handler_rest.go is missing %s: the platform still queues and reads containers here", want)
		}
	}
}

// TestPerAgentCapabilityRoutesStay: the per-agent capability editor is a different
// question from the catalogue the console manages, and an earlier cleanup answered
// both as "the catalogue moved" — it deleted these four along with the forwarders and
// the page started returning 400. The distinction is what this pins: the per-agent
// routes resolve the catalogue for one agent, so they stay here.
func TestPerAgentCapabilityRoutesStay(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/user/capabilities/agents/agent-1"},
		{http.MethodPut, "/api/v1/user/capabilities/agents/agent-1"},
		{http.MethodDelete, "/api/v1/user/capabilities/agents/agent-1"},
		{http.MethodPut, "/api/v1/user/capabilities/agents/agent-1/permissions"},
	} {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"user_id":"u-1"}`))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// A routed request reaches the handler, which answers with its own status.
		// What it must never be is the mux's bare 400/404 for an unknown path.
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Errorf("%s %s is not routed: the per-agent capability editor lost its endpoint", route.method, route.path)
		}
	}
}
