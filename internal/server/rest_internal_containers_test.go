package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The portal asks this service to enqueue provisioning rather than writing the
// queue's collection itself: one writer, one queue. Missing identifiers are a
// bad request, not a silently empty request.
func TestInternalContainerRequestValidatesItsBody(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	for _, body := range []string{`{}`, `{"user_id":"u"}`, `{"agent_id":"a"}`, `not json`} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/internal/container-requests", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, rec.Code)
		}
	}
}
