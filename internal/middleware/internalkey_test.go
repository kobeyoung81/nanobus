package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func request(method, target string, header map[string]string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	return req
}

func TestInternalKey_AcceptsBearerToken(t *testing.T) {
	key := NewInternalKey("s3cret")

	if !key.Allow(request(http.MethodGet, "/api/v1/agents", map[string]string{"Authorization": "Bearer s3cret"})) {
		t.Error("a bearer token must be accepted")
	}
	// Header name and scheme are case-insensitive per RFC 7235.
	if !key.Allow(request(http.MethodGet, "/api/v1/agents", map[string]string{"Authorization": "bearer s3cret"})) {
		t.Error("the bearer scheme must match case-insensitively")
	}
	// A WebSocket handshake from some clients cannot set headers.
	if !key.Allow(request(http.MethodGet, "/ws/hypervisor?token=s3cret", nil)) {
		t.Error("the query parameter must be accepted")
	}
}

func TestInternalKey_RejectsWrongOrMissingToken(t *testing.T) {
	key := NewInternalKey("s3cret")

	for name, req := range map[string]*http.Request{
		"missing":      request(http.MethodGet, "/api/v1/agents", nil),
		"wrong":        request(http.MethodGet, "/api/v1/agents", map[string]string{"Authorization": "Bearer nope"}),
		"other scheme": request(http.MethodGet, "/api/v1/agents", map[string]string{"Authorization": "Basic s3cret"}),
		"shorter":      request(http.MethodGet, "/api/v1/agents", map[string]string{"Authorization": "Bearer s3cre"}),
		"wrong query":  request(http.MethodGet, "/api/v1/agents?token=nope", nil),
	} {
		if key.Allow(req) {
			t.Errorf("%s: must not be allowed", name)
		}
	}
}

// A deployment without a token is misconfigured: refusing is the only safe
// answer, because accepting would leave the bus open to anything that can reach
// its port.
func TestInternalKey_UnconfiguredRefuses(t *testing.T) {
	key := NewInternalKey("")
	protected := func(*http.Request) bool { return true }
	rec := httptest.NewRecorder()

	key.Guard(okHandler(t), protected).ServeHTTP(rec, request(http.MethodPost, "/api/v1/inbound", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestInternalKey_GuardRejects(t *testing.T) {
	key := NewInternalKey("s3cret")
	protected := func(*http.Request) bool { return true }
	handler := key.Guard(okHandler(t), protected)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/v1/inbound", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: status = %d, want 401", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/v1/inbound", map[string]string{"Authorization": "Bearer nope"}))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: status = %d, want 401", rec.Code)
	}
}

func TestInternalKey_GuardPassesAuthorizedAndExempt(t *testing.T) {
	key := NewInternalKey("s3cret")
	protected := func(r *http.Request) bool { return r.URL.Path != "/ws/voice" }
	handler := key.Guard(okHandler(t), protected)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodPost, "/api/v1/inbound", map[string]string{"Authorization": "Bearer s3cret"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("authorized: status = %d, want 200", rec.Code)
	}

	// The browser-facing dictation socket authenticates itself.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, request(http.MethodGet, "/ws/voice?session=abc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("exempt path: status = %d, want 200 (no token required)", rec.Code)
	}
}
