package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/middleware"
)

// guardedMux builds the chain the server actually serves (s.handler()), with
// the token configured, so these tests cover the wiring and not just the guard.
func guardedMux(t *testing.T, s *Server, token string) http.Handler {
	t.Helper()
	s.internalKey = middleware.NewInternalKey(token)
	return s.handler()
}

// The injection hole this closes: POST /api/v1/inbound was reachable by anyone
// who could open the bus port, and one such request makes an agent run.
func TestInternalKeyGuard_RestRequiresToken(t *testing.T) {
	s := wireServer(t)
	handler := guardedMux(t, s, "s3cret")

	// A malformed body proves the request got past the guard (bad request
	// rather than unauthorized) without needing a database expectation.
	body := []byte(`{`)
	for name, header := range map[string]string{
		"no token":    "",
		"wrong token": "Bearer nope",
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound", bytes.NewReader(body))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, rec.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer s3cret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("authorized: status = %d, want the handler's 400", rec.Code)
	}
}

// The WebSocket catch-all drives the same handlers as the REST API, so it is
// protected too; leaving it open would have kept the hole.
func TestInternalKeyGuard_CatchAllRequiresToken(t *testing.T) {
	s := wireServer(t)
	handler := guardedMux(t, s, "s3cret")

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// The dictation socket is dialed by a browser (through nginx) and authenticates
// with a single-use id, so the guard must not stand in front of it.
func TestInternalKeyGuard_VoiceSocketStaysOpen(t *testing.T) {
	s := wireServer(t)
	handler := guardedMux(t, s, "s3cret")

	req := httptest.NewRequest(http.MethodGet, "/ws/voice?session=abc", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the voice handler's own answer", rec.Code)
	}
}

// A bus without a token refuses its internal endpoints instead of serving them.
func TestInternalKeyGuard_UnconfiguredRefuses(t *testing.T) {
	s := wireServer(t)
	handler := guardedMux(t, s, "")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// The directory login console is the one route that carries the *caller's* JWT
// instead of the bus token: the portal has to forward the operator's token, and
// nanoauth verifies that token and the management role on every route under the
// subtree. The guard must let it through, or the bus answers the console with a
// 401 — which the portal's browser client reads as "your session is over" and
// logs the operator out.
func TestInternalKeyGuard_DirectoryConsoleCarriesTheCallersToken(t *testing.T) {
	var seenAuth, seenPath string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer auth.Close()

	// Both spellings: the /api/auth/v1 rewrite runs after the guard.
	for _, path := range []string{"/auth/v1/directory/effective", "/api/auth/v1/directory/effective"} {
		seenAuth, seenPath = "", ""
		s := wireServer(t, WithNanoauthURL(auth.URL))
		handler := guardedMux(t, s, "s3cret")

		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer operator-jwt")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want the auth service's own answer", path, rec.Code)
		}
		if seenAuth != "Bearer operator-jwt" {
			t.Fatalf("%s: upstream Authorization = %q, want the caller's token", path, seenAuth)
		}
		if seenPath != "/auth/v1/directory/effective" {
			t.Fatalf("%s: upstream path = %q", path, seenPath)
		}
	}
}

// The exemption is exactly that subtree: login, userinfo and a sibling path that
// merely starts the same way keep requiring the internal token.
func TestInternalKeyGuard_OtherAuthRoutesStillRequireTheToken(t *testing.T) {
	var hits int
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer auth.Close()

	for _, path := range []string{"/auth/v1/token", "/auth/v1/userinfo", "/auth/v1/directory", "/auth/v1/directoryfoo"} {
		hits = 0
		s := wireServer(t, WithNanoauthURL(auth.URL))
		handler := guardedMux(t, s, "s3cret")

		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer operator-jwt")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 (a caller's token is not the bus token)", path, rec.Code)
		}
		if hits != 0 {
			t.Errorf("%s: the auth service was reached %d time(s) without the bus token", path, hits)
		}

		req = httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		rec = httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s: with the bus token status = %d, want the auth service's 200", path, rec.Code)
		}
	}
}
