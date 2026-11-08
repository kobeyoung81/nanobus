package server

import (
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// registerAuthProxy forwards /auth/v1/* requests to the nanoauth service.
// The bus intentionally contains no auth logic — nanoauth owns the users table,
// JWKS, and OAuth flows. If nanoauthURL is not configured the routes are skipped.
func (s *Server) registerAuthProxy(mux *http.ServeMux) {
	if s.nanoauthURL == "" {
		return
	}

	u, err := url.Parse(s.nanoauthURL)
	if err != nil {
		slog.Error("invalid nanoauth url", "url", s.nanoauthURL, "error", err)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(u)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Preserve the /auth/v1 prefix on the upstream path.
		req.URL.Path = strings.TrimPrefix(req.URL.Path, "/api")
		req.Host = u.Host
	}

	mux.HandleFunc("/auth/v1/", func(w http.ResponseWriter, r *http.Request) {
		slog.Debug("proxying auth request", "path", r.URL.Path)
		proxy.ServeHTTP(w, r)
	})

	// also expose under /api/auth/v1 for consistency with the other v1 endpoints
	mux.HandleFunc("/api/auth/v1/", func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = "/auth/v1/" + strings.TrimPrefix(r.URL.Path, "/api/auth/v1/")
		proxy.ServeHTTP(w, r)
	})
}

// The directory login console's subtree on this proxy. It is the one route that
// carries the *caller's* JWT rather than the internal token: the portal has to
// forward the operator's token (nanoauth re-verifies it and enforces the
// management role on every route under it), so it cannot also put the bus token
// in the same header. The guard therefore lets these paths through — see
// Server.protectedRequest. Both spellings are listed because the /api/auth/v1
// rewrite above runs *after* the guard.
const (
	authDirectoryPath      = "/auth/v1/directory/"
	authDirectoryPathAlias = "/api/auth/v1/directory/"
)

// isDirectoryConsolePath reports whether a request is one of the directory
// console's calls (prefix match, so only that subtree is exempt).
func isDirectoryConsolePath(path string) bool {
	return strings.HasPrefix(path, authDirectoryPath) || strings.HasPrefix(path, authDirectoryPathAlias)
}
