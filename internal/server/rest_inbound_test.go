package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
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
