package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

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

// recordAudit writes an audit entry for a mutating request.
func (s *Server) recordAudit(r *http.Request, action, resourceType, resourceID string, detail map[string]any) {
	entry := store.AuditEntry{
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
	if err := s.store.RecordAudit(entry); err != nil {
		slog.Warn("record audit failed", "action", action, "error", err)
	}
}

func (s *Server) restListAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.store.ListAudit(store.AuditFilter{
		Limit:        limit,
		Action:       searchTerm(r, "action"),
		Actor:        searchTerm(r, "actor"),
		ResourceType: searchTerm(r, "resource_type"),
		Resource:     searchTerm(r, "resource"),
	})
	if err != nil {
		slog.Error("list audit", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	entries = nonNilSlice(entries)
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// restRecordAudit appends one entry to the trail.
//
// The portal writes these when an administrator does something; it used to do it
// with its own INSERT, which made the trail's schema a contract between the two
// services. The entry's fields are the portal's, and the timestamp is the bus's.
func (s *Server) restRecordAudit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ActorID      string         `json:"actor_id"`
		ActorRole    string         `json:"actor_role"`
		Action       string         `json:"action"`
		ResourceType string         `json:"resource_type"`
		ResourceID   string         `json:"resource_id"`
		Detail       map[string]any `json:"detail,omitempty"`
		IP           string         `json:"ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.Action == "" || body.ResourceType == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action and resource_type are required"})
		return
	}
	if body.ActorID == "" {
		body.ActorID = "system"
	}
	if body.ActorRole == "" {
		body.ActorRole = "service"
	}

	if err := s.store.RecordAudit(store.AuditEntry{
		ActorID: body.ActorID, ActorRole: body.ActorRole, Action: body.Action,
		ResourceType: body.ResourceType, ResourceID: body.ResourceID,
		Detail: body.Detail, IP: body.IP,
	}); err != nil {
		slog.Error("record audit", "action", body.Action, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "recorded"})
}

