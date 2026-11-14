// Package middleware provides HTTP middleware for the bus REST API.
package middleware

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// RateLimiter implements a fixed-window rate limit per client key.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	limit   int
	window  time.Duration
	// OnLimited, when set, is called for every refused request. The server points
	// it at the metrics counter; a limiter built in a test leaves it nil.
	OnLimited func()
}

type bucket struct {
	count   int
	resetAt time.Time
}

// NewRateLimiter returns a limiter allowing `limit` requests per `window`.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*bucket),
		limit:   limit,
		window:  window,
	}
}

// Allow reports whether a request for the given key is within the limit.
func (rl *RateLimiter) Allow(key string) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[key]
	if !ok || now.After(b.resetAt) {
		rl.buckets[key] = &bucket{count: 1, resetAt: now.Add(rl.window)}
		return true
	}
	b.count++
	return b.count <= rl.limit
}

// Middleware wraps a handler with per-client-IP rate limiting.
//
// A request the exempt predicate accepts is passed through and not counted: the
// bus's own services share one client address, and throttling them would throttle
// every user behind them at once.
func (rl *RateLimiter) Middleware(next http.Handler, exempt func(*http.Request) bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if exempt != nil && exempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !rl.Allow(clientIP(r)) {
			if rl.OnLimited != nil {
				rl.OnLimited()
			}
			w.Header().Set("Retry-After", fmtDuration(rl.window))
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if ip, _, err := net.SplitHostPort(forwarded); err == nil {
			return ip
		}
		return forwarded
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return "1"
	}
	return strconv.Itoa(int(d.Seconds()))
}
