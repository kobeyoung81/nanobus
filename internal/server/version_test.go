package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A probe must be able to say which build it is talking to: the image tag and the
// label are on the outside, and this is the inside.
func TestHealthzReportsTheBuildVersion(t *testing.T) {
	previous := Version
	Version = "0.1.1"
	t.Cleanup(func() { Version = previous })

	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if body["status"] != "ok" || body["version"] != "0.1.1" {
		t.Fatalf("body = %v, want status ok and version 0.1.1", body)
	}
	if !strings.Contains(rec.Body.String(), `"version":"0.1.1"`) {
		t.Fatalf("raw body = %s", rec.Body.String())
	}
}
