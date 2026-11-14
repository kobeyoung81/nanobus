package server

// Shared helpers for the tests whose subject is a route: one builds the mux the
// server registers, the other sends a request through it.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
)

// authCall records what one forwarded call looked like on the wire.
type authCall struct{ method, path, body, auth string }

// authStub stands in for nanoauth: it records the calls the bus forwards and
// answers with the given status and body.
func authStub(t *testing.T, calls *[]authCall, status int, answer string) *authclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*calls = append(*calls, authCall{r.Method, r.URL.RequestURI(), string(body), r.Header.Get("Authorization")})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	return authclient.New(srv.URL, "test-token")
}

// accountRequest sends one request through the routing table.
func accountRequest(t *testing.T, s *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)
	return rec
}
