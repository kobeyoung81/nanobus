package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiter_Allow(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)

	for i := 0; i < 3; i++ {
		if !rl.Allow("a") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	if rl.Allow("a") {
		t.Fatal("4th request should be denied")
	}
	if !rl.Allow("b") {
		t.Fatal("different key should be allowed")
	}
}

func TestRateLimiter_WindowReset(t *testing.T) {
	rl := NewRateLimiter(1, 50*time.Millisecond)

	if !rl.Allow("a") {
		t.Fatal("first should be allowed")
	}
	if rl.Allow("a") {
		t.Fatal("second within window should be denied")
	}
	time.Sleep(60 * time.Millisecond)
	if !rl.Allow("a") {
		t.Fatal("after window expiry should be allowed")
	}
}

func TestRateLimiter_Middleware(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	req.RemoteAddr = "10.0.0.1:1234"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", rec.Code)
	}
}

// TestRateLimiter_ExemptRequestsAreNotCounted: the platform's own services share
// one address, so the exemption must both pass the call through and leave the
// budget alone — otherwise one busy gateway exhausts the limit for every browser
// behind it.
func TestRateLimiter_ExemptRequestsAreNotCounted(t *testing.T) {
	rl := NewRateLimiter(1, time.Minute)
	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer trusted" })

	trusted := httptest.NewRequest(http.MethodGet, "/api/v1/inbound", nil)
	trusted.RemoteAddr = "10.0.0.9:1234"
	trusted.Header.Set("Authorization", "Bearer trusted")
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, trusted)
		if rec.Code != http.StatusOK {
			t.Fatalf("trusted call %d: status = %d", i, rec.Code)
		}
	}

	stranger := httptest.NewRequest(http.MethodGet, "/api/v1/inbound", nil)
	stranger.RemoteAddr = "10.0.0.9:1234"
	if rec := httptest.NewRecorder(); func() int {
		handler.ServeHTTP(rec, stranger)
		return rec.Code
	}() != http.StatusOK {
		t.Fatal("the stranger lost its first request to the trusted caller's traffic")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, stranger)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second untrusted call = %d, want 429", rec.Code)
	}
}
