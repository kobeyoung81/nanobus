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

	"gitee.com/kobeyoung81/nanobus/internal/adminclient"
	"gitee.com/kobeyoung81/nanobus/internal/authclient"
	"gitee.com/kobeyoung81/nanobus/internal/metrics"
	"gitee.com/kobeyoung81/nanobus/internal/middleware"
	"gitee.com/kobeyoung81/nanobus/internal/portalclient"
	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/stream"
	"github.com/gorilla/websocket"
)

// Server is the bus WebSocket server.
type Server struct {
	store       *store.Store
	upgrader    websocket.Upgrader
	listener    net.Listener
	srv         *http.Server
	nanoauthURL string
	auth        *authclient.Client
	admin       *adminclient.Client
	portal      *portalclient.Client
	provisioner *provisionerclient.Client
	chunks      *stream.ChunkBuffer
	// streams is the durable, per-conversation event log. The in-memory ring
	// above stays for gateways that still subscribe per channel.
	streams   streamEventStore
	coalescer *stream.Coalescer
	// eventTailer follows the event log and fans it out to this replica's
	// subscribers. One per replica: the database sees a single reader whatever
	// the number of open streams.
	eventTailer *stream.Tailer[stream.Event]
	// platform is the durable change-notification log (config, capabilities,
	// agents); voiceStore holds the one-time voice ids. Both let a second
	// replica serve what the first one published.
	platform          platformEventStore
	platformTailer    *stream.Tailer[stream.PlatformEvent]
	voiceStore        voiceSessionStore
	sse               *sseHub
	events            *sseHub
	hypervisorTunnels *hypervisorTunnelRegistry
	terminalSubs      *terminalSubRegistry
	// agentSockets are this replica's agent WebSocket connections. Pushes come
	// from the tailers below, so no routing between replicas is needed.
	agentSockets *agentSocketRegistry
	// inboundTailer follows the inbound queue: an insert means "an agent has
	// something to claim".
	inboundTailer   *stream.Tailer[stream.InboundNotice]
	voiceSessions   *voiceSessionRegistry
	rateLimiter     *middleware.RateLimiter
	internalKey     *middleware.InternalKey
	internalToken   string
	maxContentBytes int
	tailerCancel    context.CancelFunc
	mu              sync.Mutex
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

// WithAuthURL sets the same base URL for the internal account surface. The two
// differ in what they address, not where: the proxy forwards a user's token, the
// client presents the platform's shared one.
func WithAuthURL(url, token string) ServerOption {
	return func(s *Server) {
		s.auth = authclient.New(url, token)
	}
}

// WithAdminURL sets the admin module's base URL. The bus does not serve
// configuration any more; it reads the voice component from here for its own
// user-facing endpoint.
func WithAdminURL(url, token string) ServerOption {
	return func(s *Server) {
		s.admin = adminclient.New(url, token)
	}
}

// WithMaxContentBytes sets the maximum accepted message content size.
// WithPortalURL sets the portal's base URL. The agent rows belong to the portal
// now; the bus forwards the two writes its protocol surface still receives
// (a runtime status and a deleted type's reference cleanup) there.
func WithPortalURL(url, token string) ServerOption {
	return func(s *Server) {
		s.portal = portalclient.New(url, token)
	}
}

// WithProvisionerURL sets the runner's base URL. The provisioning queue belongs
// to it now; the console's container actions are forwarded there.
func WithProvisionerURL(url, token string) ServerOption {
	return func(s *Server) {
		s.provisioner = provisionerclient.New(url, token)
	}
}

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
		// Kept as a plain string too: forwarding a call to another module needs
		// the token itself, not just the ability to check one.
		s.internalToken = token
	}
}

// NewServer creates a new bus Server backed by the given store.
func NewServer(s *store.Store, opts ...ServerOption) *Server {
	eventStore := streamStoreFor(s)
	platformStore := platformStoreFor(s)
	server := &Server{
		store: s,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		chunks:            stream.NewChunkBuffer(1000),
		platform:          nil,
		voiceStore:        voiceStoreFor(s),
		sse:               newSSEHub(),
		events:            newSSEHub(),
		hypervisorTunnels: newHypervisorTunnelRegistry(),
		terminalSubs:      newTerminalSubRegistry(),
		agentSockets:      newAgentSocketRegistry(),
		voiceSessions:     newVoiceSessionRegistry(voiceSessionTTL),
		maxContentBytes:   DefaultMaxContentBytes,
	}
	// A bare *stream.Store in an interface field is not nil: assign only when
	// the database exists, so every "is durable streaming available?" check
	// keeps working.
	if eventStore != nil {
		server.streams = eventStore
		server.coalescer = coalescerFor(eventStore)
		server.eventTailer = stream.NewEventTailer(eventStore)
	}
	if platformStore != nil {
		server.platform = platformStore
		server.platformTailer = stream.NewPlatformTailer(platformStore)
	}
	if s != nil {
		server.inboundTailer = stream.NewInboundTailer(s.InboundQueueCollection())
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

	// Operational numbers for a scraper. Internal network, anonymous (D7).
	mux.Handle("GET /metrics", metrics.Handler())

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

	s.startTailers()
	s.srv = &http.Server{Handler: s.handler()}

	return s.srv.Serve(ln)
}

// protectedRequest reports whether a request must present the internal token.
//
// The bus is an internal service: its REST API and the tunnel/hypervisor/
// terminal sockets are dialed only by agent containers, the gateway, the runner
// and the portal. Two routes are exempt:
//
//   - the dictation socket, which is served to a browser and authenticates with
//     a single-use id minted through the portal.
//
// The auth proxy that used to live here is gone: nginx sends /api/v1/auth/* and
// /api/auth/v1/* straight to nanoauth, so the bus has no browser-facing route left
// (the directory console included). Its own `validate_token` call is an RPC, not a
// proxy.
//
// Anything else — including the WebSocket catch-all, which drives the same
// handlers as the REST API — requires the token.
func (s *Server) protectedRequest(r *http.Request) bool {
	// /metrics is an anonymous scrape inside the internal network (D7);
	// /healthz is what an orchestrator's probe calls, and a probe has no token.
	if r.URL.Path == "/metrics" || r.URL.Path == "/healthz" {
		return false
	}
	return r.URL.Path != voiceWSPath
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

// startTailers runs one tailer per collection for this replica.
func (s *Server) startTailers() {
	if s.eventTailer == nil && s.platformTailer == nil && s.inboundTailer == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.tailerCancel = cancel
	s.mu.Unlock()
	if s.eventTailer != nil {
		go s.eventTailer.Run(ctx)
	}
	if s.platformTailer != nil {
		go s.platformTailer.Run(ctx)
	}
	if s.inboundTailer != nil {
		go s.inboundTailer.Run(ctx)
	}
	if s.inboundTailer != nil {
		go s.consumeInboundNotices(ctx)
	}
	if s.platformTailer != nil {
		go s.consumePlatformNotices(ctx)
	}
}

// Close gracefully shuts down the server.
func (s *Server) Close() error {
	// Flush buffered stream writes before the process goes away.
	if s.coalescer != nil {
		s.coalescer.FlushAll()
	}
	s.mu.Lock()
	if s.tailerCancel != nil {
		s.tailerCancel()
		s.tailerCancel = nil
	}
	s.mu.Unlock()
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
