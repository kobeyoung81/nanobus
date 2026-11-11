package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/adminclient"
)

// auditWriteTimeout bounds one best-effort append. The write must not hold a
// handler open behind a slow admin module: the action has already happened.
const auditWriteTimeout = 5 * time.Second

// clientIP extracts the client IP, honoring X-Forwarded-For.
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

// recordAudit reports one of the bus's own actions to the audit trail.
//
// The trail is the admin module's collection; this handler forwards, and a
// failure is a warning rather than an error — the action it describes has
// already been carried out.
func (s *Server) recordAudit(r *http.Request, action, resourceType, resourceID string, detail map[string]any) {
	entry := adminclient.AuditEntry{
		ActorID:      r.Header.Get("X-Admin-User"),
		ActorRole:    r.Header.Get("X-Admin-Role"),
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Detail:       detail,
		IP:           clientIP(r),
	}
	if entry.ActorID == "" {
		entry.ActorID = "system"
	}
	if entry.ActorRole == "" {
		entry.ActorRole = "service"
	}
	if s.admin == nil || !s.admin.Enabled() {
		slog.Warn("record audit skipped: admin module is not configured", "action", action)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), auditWriteTimeout)
	defer cancel()
	if err := s.admin.RecordAudit(ctx, entry); err != nil {
		slog.Warn("record audit failed", "action", action, "error", err)
	}
}

// restListAudit forwards the trail to the admin module, which owns it.
//
// The route is kept so a caller that still addresses the bus keeps working; the
// console asks the admin module directly through the portal.
func (s *Server) restListAudit(w http.ResponseWriter, r *http.Request) {
	path := "/api/v1/audit"
	if query := r.URL.RawQuery; query != "" {
		path += "?" + query
	}
	s.forwardToAdmin(w, r, http.MethodGet, path)
}

// restRecordAudit forwards one append to the admin module. The body is the
// entry shape both faces share; the admin module assigns the id and the time.
func (s *Server) restRecordAudit(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, http.MethodPost, "/api/v1/internal/audit")
}
