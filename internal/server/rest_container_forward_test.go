package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
)

// The queue is the runner's now. The bus no longer carries the console's
// container lifecycle actions (they belong to the admin module), but it still
// forwards the requests the platform makes — an agent's container request, the
// runner registry — and passes the runner's own verdict through where it means
// something (404/409) instead of flattening everything into a gateway error.

// The enqueue path the portal uses.
func TestContainerRequestForwardedToTheProvisioner(t *testing.T) {
	var gotPath, gotToken string
	provisioner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer provisioner.Close()

	s := NewServer(nil, WithProvisionerURL(provisioner.URL, "tok"))
	rec := httptest.NewRecorder()
	s.restInternalContainerRequest(rec, httptest.NewRequest(http.MethodPost, "/api/v1/internal/container-requests",
		strings.NewReader(`{"user_id":"u1","agent_id":"a1"}`)))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if gotPath != "/api/v1/internal/container-requests" {
		t.Fatalf("forwarded to %q", gotPath)
	}
	if gotToken != "Bearer tok" {
		t.Fatalf("token = %q", gotToken)
	}
}

// "Nothing to act on" is the runner's 404, not a gateway failure.
func TestProvisionerNotFoundPassesThrough(t *testing.T) {
	provisioner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"container request not found"}`, http.StatusNotFound)
	}))
	defer provisioner.Close()

	s := NewServer(nil, WithProvisionerURL(provisioner.URL, ""))
	rec := httptest.NewRecorder()
	s.restRequestContainer(rec, httptest.NewRequest(http.MethodPost, "/api/v1/containers",
		strings.NewReader(`{"user_id":"missing"}`)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

// Without a configured runner the action must fail loudly rather than silently
// doing nothing.
func TestContainerActionWithoutProvisionerFails(t *testing.T) {
	s := NewServer(nil)
	err := s.callProvisioner(httptest.NewRequest(http.MethodPost, "/api/v1/containers", nil),
		func(ctx context.Context, c *provisionerclient.Client) error { return nil })
	if err == nil {
		t.Fatal("expected an error without a configured provisioner")
	}
	rec := httptest.NewRecorder()
	s.writeProvisionerError(rec, err, "dispose container", "alice")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}
