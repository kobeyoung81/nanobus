// Package server implements the bus WebSocket server that routes
// messages between gateways and agents via MongoDB-backed queues.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/middleware"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/stream"
	"github.com/gorilla/websocket"
)

// Server is the bus WebSocket server.
type Server struct {
	store             *store.Store
	upgrader          websocket.Upgrader
	listener          net.Listener
	srv               *http.Server
	nanoauthURL       string
	chunks            *stream.ChunkBuffer
	sse               *sseHub
	events            *sseHub
	tunnels           *TunnelRegistry
	hypervisorTunnels *hypervisorTunnelRegistry
	terminalSubs      *terminalSubRegistry
	voiceSessions     *voiceSessionRegistry
	rateLimiter       *middleware.RateLimiter
	internalKey       *middleware.InternalKey
	maxContentBytes   int
	mu                sync.Mutex
	sweeperStop       chan struct{}
	stopOnce          sync.Once
}

// DefaultMaxContentBytes bounds a single message's content when no option is
// supplied.
const DefaultMaxContentBytes = 16*1024*1024 - 1

// ContentTooLargeError reports a message whose content exceeds the configured
// cap. Callers map it to HTTP 413 / an RPC error so the client sees why the
// write was rejected (instead of the store refusing it as an opaque write error).
type ContentTooLargeError struct {
	Size int
	Max  int
}

func (e *ContentTooLargeError) Error() string {
	return fmt.Sprintf("content too large: %d bytes exceeds max %d", e.Size, e.Max)
}

// checkContent enforces the configured content cap.
func (s *Server) checkContent(content string) error {
	if s.maxContentBytes > 0 && len(content) > s.maxContentBytes {
		return &ContentTooLargeError{Size: len(content), Max: s.maxContentBytes}
	}
	return nil
}

// maxBodySlack is extra room above the content cap for JSON framing when
// bounding a request body.
const maxBodySlack = 1 << 20

// writeBodyTooLarge writes a 413 and reports true when err is the http
// package's body-limit error.
func writeBodyTooLarge(w http.ResponseWriter, err error) bool {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
		return true
	}
	return false
}

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithNanoauthURL sets the upstream nanoauth base URL proxied under /auth/v1.
func WithNanoauthURL(url string) ServerOption {
	return func(s *Server) {
		s.nanoauthURL = url
	}
}

// WithMaxContentBytes sets the maximum accepted message content size.
func WithMaxContentBytes(n int) ServerOption {
	return func(s *Server) {
		if n > 0 {
			s.maxContentBytes = n
		}
	}
}

// WithRateLimit enables per-client-IP rate limiting on the REST API.
func WithRateLimit(limit int, window time.Duration) ServerOption {
	return func(s *Server) {
		s.rateLimiter = middleware.NewRateLimiter(limit, window)
	}
}

// WithInternalToken requires the shared internal token on the bus's internal
// endpoints (every REST route and the sockets the platform's own processes
// dial). An empty token is a configuration mistake: the bus then refuses those
// requests instead of serving them unauthenticated.
func WithInternalToken(token string) ServerOption {
	return func(s *Server) {
		s.internalKey = middleware.NewInternalKey(token)
	}
}

// NewServer creates a new bus Server backed by the given store.
func NewServer(s *store.Store, opts ...ServerOption) *Server {
	server := &Server{
		store: s,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		chunks:            stream.NewChunkBuffer(1000),
		sse:               newSSEHub(),
		events:            newSSEHub(),
		tunnels:           NewTunnelRegistry(),
		hypervisorTunnels: newHypervisorTunnelRegistry(),
		terminalSubs:      newTerminalSubRegistry(),
		voiceSessions:     newVoiceSessionRegistry(voiceSessionTTL),
		maxContentBytes:   DefaultMaxContentBytes,
		sweeperStop:       make(chan struct{}),
	}
	for _, opt := range opts {
		opt(server)
	}
	return server
}

// handler builds the HTTP chain: the routes, then the internal-key guard, then
// the rate limiter. The limiter is outermost so even an unauthenticated flood
// is bounded per IP, and a rejected request never reaches a handler.
func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	// REST API endpoints (registered before catch-all WS handler)
	s.registerRESTRoutes(mux)

	// WebSocket catch-all (legacy compatibility + future WS clients)
	mux.HandleFunc("/", s.handleHTTP)

	var h http.Handler = mux
	if s.internalKey != nil {
		h = s.internalKey.Guard(h, s.protectedRequest)
	}
	if s.rateLimiter != nil {
		h = s.rateLimiter.Middleware(h)
	}
	return h
}

// ListenAndServe starts the server on the given address with both WebSocket and REST endpoints.
func (s *Server) ListenAndServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	slog.Info("bus started", "addr", ln.Addr().String())

	s.srv = &http.Server{Handler: s.handler()}

	// Start the runner-uptime sweeper (marks stale runners offline).
	go s.runRunnerSweeper(s.sweeperStop)

	return s.srv.Serve(ln)
}

// protectedRequest reports whether a request must present the internal token.
//
// The bus is an internal service: its REST API and the tunnel/hypervisor/
// terminal sockets are dialed only by agent containers, the gateway, the runner
// and the portal. Two routes are exempt:
//
//   - the dictation socket, which is served to a browser and authenticates with
//     a single-use id minted through the portal;
//   - the directory login console's subtree, which is the one request that must
//     carry the *caller's* JWT in Authorization — the portal cannot put the
//     internal token there as well, and nanoauth verifies that JWT and the
//     management role itself on every route under it.
//
// Anything else — including the WebSocket catch-all, which drives the same
// handlers as the REST API — requires the token.
func (s *Server) protectedRequest(r *http.Request) bool {
	return r.URL.Path != voiceWSPath && !isDirectoryConsolePath(r.URL.Path)
}

// runRunnerSweeper periodically marks runners whose heartbeat has gone stale
// as offline. It never deletes rows; operators prune dead runners explicitly.
func (s *Server) runRunnerSweeper(stop <-chan struct{}) {
	if err := s.store.MarkStaleRunnersOffline(); err != nil {
		slog.Warn("runner sweeper: initial sweep failed", "error", err)
	}

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := s.store.MarkStaleRunnersOffline(); err != nil {
				slog.Warn("runner sweeper: sweep failed", "error", err)
			}
		}
	}
}

// Addr returns the listener address, or empty string if not listening.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Close gracefully shuts down the server.
func (s *Server) Close() error {
	s.stopOnce.Do(func() {
		close(s.sweeperStop)
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return s.srv.Shutdown(context.Background())
	}
	return nil
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	slog.Debug("new websocket connection", "remote", r.RemoteAddr)
	s.handleConnection(conn)
}
