package server

// The REST limiter, wired: off unless asked for, per client address, and never
// counting the platform's own callers.
//
// The exemption is the whole point. Every service in the stack reaches the bus
// from one container address; a limiter that counted them would turn one busy
// gateway into a platform-wide outage, and the failure would look like "the
// bus is down" rather than "the budget is misconfigured".

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func limitedServer(t *testing.T, limit int) http.Handler {
	t.Helper()
	t.Setenv("NANOSEC_BUS_INTERNAL_TOKEN", "s3cret")
	s := NewServer(nil,
		WithInternalToken("s3cret"),
		WithRateLimit(limit, time.Minute),
	)
	return s.handler()
}

func get(h http.Handler, path, addr, token string) int {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = addr
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestRateLimitRefusesTheSecondCallFromOneClient(t *testing.T) {
	h := limitedServer(t, 1)

	if code := get(h, "/api/v1/agent-fleet", "10.0.0.1:1000", ""); code == http.StatusTooManyRequests {
		t.Fatal("the first call was limited")
	}
	if code := get(h, "/api/v1/agent-fleet", "10.0.0.1:1000", ""); code != http.StatusTooManyRequests {
		t.Fatalf("second call = %d, want 429", code)
	}
	// A different client has its own budget.
	if code := get(h, "/api/v1/agent-fleet", "10.0.0.2:1000", ""); code == http.StatusTooManyRequests {
		t.Fatal("another client was charged for the first one's traffic")
	}
}

// TestRateLimitDoesNotCountPlatformCallers: the internal token is the platform's
// own identity, and its services share an address.
func TestRateLimitDoesNotCountPlatformCallers(t *testing.T) {
	h := limitedServer(t, 1)

	for i := 0; i < 5; i++ {
		if code := get(h, "/api/v1/agent-fleet", "10.0.0.1:1000", "s3cret"); code == http.StatusTooManyRequests {
			t.Fatalf("platform call %d was limited", i)
		}
	}
	// Their traffic did not consume the browser budget either.
	if code := get(h, "/api/v1/agent-fleet", "10.0.0.1:1000", ""); code == http.StatusTooManyRequests {
		t.Fatal("the first browser call was refused after platform traffic")
	}
}

// TestRateLimitExemptsProbes: a scrape every few seconds is not abuse, and a
// throttled health check would take the replica out of rotation.
func TestRateLimitExemptsProbes(t *testing.T) {
	h := limitedServer(t, 1)

	for i := 0; i < 5; i++ {
		if code := get(h, "/metrics", "10.0.0.1:1000", ""); code == http.StatusTooManyRequests {
			t.Fatalf("/metrics call %d was limited", i)
		}
		if code := get(h, "/healthz", "10.0.0.1:1000", ""); code == http.StatusTooManyRequests {
			t.Fatalf("/healthz call %d was limited", i)
		}
	}
}
