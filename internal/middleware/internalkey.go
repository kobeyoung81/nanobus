// Internal-key authentication for the bus's internal endpoints.
package middleware

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"sync"
)

// InternalKey authenticates the bus's internal callers — agent containers, the
// gateway, the runner and the portal — with one shared token. They all run in
// the same trust domain and are provisioned together, so a shared secret keeps
// the change to "send this header" instead of per-service identities; what it
// buys is that the bus is no longer open to anything that can reach its port.
type InternalKey struct {
	key string
	// warnOnce keeps the "not configured" error to one line per process: it is
	// a deployment mistake, not a per-request condition.
	warnOnce sync.Once
}

// NewInternalKey returns a guard for the given token. An empty token is a
// configuration mistake: the guard then refuses every protected request rather
// than silently leaving the bus open.
func NewInternalKey(key string) *InternalKey {
	return &InternalKey{key: key}
}

// Allow reports whether a request carries the configured token. The token
// travels in the Authorization header; the query parameter is accepted because
// a WebSocket handshake from some clients has no other place to put it.
func (k *InternalKey) Allow(r *http.Request) bool {
	if k.key == "" {
		return false
	}
	token := bearerToken(r)
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(k.key)) == 1
}

// Guard wraps next, requiring the token for requests protected() claims.
// Requests it does not claim pass through untouched, so browser-facing
// endpoints that authenticate themselves are unaffected.
func (k *InternalKey) Guard(next http.Handler, protected func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !protected(r) {
			next.ServeHTTP(w, r)
			return
		}
		if k.key == "" {
			k.warnOnce.Do(func() {
				slog.Error("bus: internal endpoints have no token configured and are refusing requests",
					"hint", "set the bus internal token (NANOSEC_BUS_INTERNAL_TOKEN)")
			})
			writeAuthError(w, http.StatusServiceUnavailable, "bus authentication is not configured")
			return
		}
		if !k.Allow(r) {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the token from an "Authorization: Bearer <token>" header.
func bearerToken(r *http.Request) string {
	value := r.Header.Get("Authorization")
	const prefix = "bearer "
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(value[len(prefix):])
}

// writeAuthError answers a rejected request. It is JSON like the rest of the
// bus API (this package cannot use the server's helper without importing it).
func writeAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + message + `"}`))
}
