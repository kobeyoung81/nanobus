package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// turnRunningStaleAfter bounds how long a persisted turn_running flag is
// trusted. If an agent crashes mid-turn the flag would otherwise stick forever;
// past this age readers treat the turn as finished.
//
// The agent refreshes the flag while a turn is in flight: it snapshots the
// session every 2s and the store flushes to the database on its own interval
// (agent setting sessionSyncFlushInterval, 1s by default), so a healthy turn
// writes turn_updated_at at least every ~3s. 15s is a 5x margin over that; it
// used to be two minutes, which meant a crashed agent kept the portal's running
// indicator on for two minutes.
//
// This is a tolerance, not a poll interval: the portal asks every few seconds and
// this is how long a frozen flag is still believed. Raising
// sessionSyncFlushInterval much above ~10s would need this raised with it.
//
// Every read uses it — the single-session read, the agent's session list and the
// attention snapshot — so no path can report a crashed turn as running.
const turnRunningStaleAfter = 15 * time.Second

// turnRunning reports whether a session's turn is currently running, treating a
// stale flag (agent crashed) as not running.
func turnRunning(running bool, updatedAt time.Time) bool {
	if !running {
		return false
	}
	if updatedAt.IsZero() {
		return true
	}
	return time.Since(updatedAt) < turnRunningStaleAfter
}

func (s *Server) restListSessions(w http.ResponseWriter, r *http.Request) {
	agentID := r.URL.Query().Get("agent_id")
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_id query param is required"})
		return
	}

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			offset = parsed
		}
	}

	sessions, err := s.store.ListSessions(agentID, limit, offset)
	if err != nil {
		slog.Error("list sessions", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	type sessionMeta struct {
		ID           string `json:"id"`
		AgentID      string `json:"agent_id"`
		AccountID    string `json:"account_id"`
		Channel      string `json:"channel"`
		ChatID       string `json:"chat_id"`
		Status       string `json:"status"`
		Title        string `json:"title"`
		MessageCount int    `json:"message_count"`
		TurnRunning  bool   `json:"turn_running"`
		CreatedAt    string `json:"created_at"`
		UpdatedAt    string `json:"updated_at"`
	}

	result := make([]sessionMeta, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, sessionMeta{
			ID:           session.ID,
			AgentID:      session.AgentID,
			AccountID:    session.AccountID,
			Channel:      session.Channel,
			ChatID:       session.ChatID,
			Status:       session.Status,
			Title:        session.Title,
			MessageCount: session.MessageCount,
			TurnRunning:  turnRunning(session.TurnRunning, session.TurnUpdatedAt),
			CreatedAt:    session.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UpdatedAt:    session.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}

	total, err := s.store.CountSessions(agentID)
	if err != nil {
		slog.Error("count sessions", "error", err)
		total = offset + len(result)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id": agentID,
		"sessions": result,
		"count":    len(result),
		"total":    total,
		"has_more": offset+len(result) < total,
	})
}

// restAgentsAttention reports the two things the portal draws as attention dots
// — a running turn and an unanswered question/approval — for every conversation
// of the caller's agents. It exists so a phone can show which conversation (and
// which agent) needs the user without opening it: the session list carries
// turn_running but not the pending markers, and reading each agent's list would
// cost a list plus a count query per agent per round.
//
// `awaiting` is deliberately not aged out: a conversation may legitimately wait
// for an answer for hours without writing anything, and a time-based rule would
// hide exactly the state the user has to act on. `running` keeps the same
// freshness window as every other read.
func (s *Server) restAgentsAttention(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id query param is required"})
		return
	}

	conversations, err := s.store.ListConversationAttention(userID)
	if err != nil {
		slog.Error("list conversation attention", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	type attentionEntry struct {
		AgentID   string `json:"agent_id"`
		ChatID    string `json:"chat_id"`
		SessionID string `json:"session_id"`
		Running   bool   `json:"running"`
		Awaiting  bool   `json:"awaiting"`
		// UpdatedAt is the server timestamp of the newest segment, RFC3339Nano.
		// Empty when the row predates the field; the browser then reports no
		// unread rather than inventing one from its own clock.
		UpdatedAt string `json:"updated_at"`
	}

	result := make([]attentionEntry, 0, len(conversations))
	for _, conversation := range conversations {
		result = append(result, attentionEntry{
			AgentID:   conversation.AgentID,
			ChatID:    conversation.ChatID,
			SessionID: conversation.SessionID,
			Running:   turnRunning(conversation.TurnRunning, conversation.TurnUpdatedAt),
			Awaiting:  metaHasPending(conversation.Meta),
			UpdatedAt: formatAttentionTime(conversation.UpdatedAt),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"conversations": result})
}

// formatAttentionTime renders a server timestamp in the same shape the rest of
// the API uses, and an empty string for a zero time.
func formatAttentionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// metaHasPending reports whether a stored meta blob carries an unanswered
// question or approval. The blob maps keys to JSON-encoded strings, and an empty
// string is what nanosec writes when a prompt is resolved; a missing key or a
// malformed blob simply has none.
func metaHasPending(meta string) bool {
	if meta == "" {
		return false
	}
	var fields map[string]string
	if json.Unmarshal([]byte(meta), &fields) != nil {
		return false
	}
	return fields["_pending_question"] != "" || fields["_pending_approval"] != ""
}

// restGetSession returns a single session.
//
//   - `?meta=1` returns metadata only, never loading the messages blob. Callers
//     that just need identity/ownership (the gateway's resume check) use it so a
//     multi-MiB conversation is not transferred on their behalf.
//   - With no `limit` it returns the full message history (back-compat).
//   - With `limit` present it returns a tail page of the message array, walking
//     backwards via the `before` cursor, so long conversations can be loaded
//     newest-first.
//
// Paginated responses carry an ETag derived from the stored metadata; a request
// whose If-None-Match matches it is answered 304 without reading the blob.
func (s *Server) restGetSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session id is required"})
		return
	}

	// The metadata read answers the meta-only request and the validators below
	// without touching the (possibly multi-MiB) messages blob.
	head, err := s.store.GetSessionMeta(sessionID)
	if err != nil {
		slog.Error("get session meta", "session_id", sessionID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if head == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	// Storable by the browser, but only after revalidation: with the ETag below a
	// reload of an unchanged conversation costs one conditional request and no
	// body (and the blob is never read). "private" keeps it out of shared caches,
	// because the response belongs to one user.
	w.Header().Set("Cache-Control", "private, no-cache")

	if r.URL.Query().Get("meta") == "1" {
		writeJSON(w, http.StatusOK, sessionResponse(head))
		return
	}

	etag := sessionETag(head)
	w.Header().Set("ETag", etag)
	if matchesETag(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	rec, err := s.store.GetSession(sessionID)
	if err != nil {
		slog.Error("get session", "session_id", sessionID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if rec == nil {
		// Archived/deleted between the two reads.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}

	resp := sessionResponse(rec)

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit == 0 {
		resp["messages"] = rec.Messages
		writeJSON(w, http.StatusOK, resp)
		return
	}

	before := -1
	if raw := r.URL.Query().Get("before"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			before = n
		}
	}
	page, total, nextBefore, hasMore := sliceMessages(rec.Messages, limit, before)
	resp["messages"] = page
	resp["total"] = total
	resp["before"] = nextBefore
	resp["has_more"] = hasMore

	writeJSON(w, http.StatusOK, resp)
}

// sessionResponse is the session shape shared by the metadata and history
// reads; callers add the `messages` field when they have it.
func sessionResponse(rec *store.SessionRecord) map[string]any {
	return map[string]any{
		"session_id":     rec.ID,
		"agent_id":       rec.AgentID,
		"account_id":     rec.AccountID,
		"channel":        rec.Channel,
		"chat_id":        rec.ChatID,
		"status":         rec.Status,
		"title":          rec.Title,
		"summary":        rec.Summary,
		"meta":           rec.Meta,
		"message_count":  rec.MessageCount,
		"messages_bytes": rec.MessagesBytes,
		"turn_running":   turnRunning(rec.TurnRunning, rec.TurnUpdatedAt),
		"created_at":     rec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"updated_at":     rec.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// sessionETag fingerprints a stored session so an unchanged conversation can be
// answered with 304 instead of transferring and re-parsing its messages.
func sessionETag(rec *store.SessionRecord) string {
	return fmt.Sprintf("W/\"%d-%d-%d\"", rec.MessageCount, rec.MessagesBytes, rec.UpdatedAt.Unix())
}

// matchesETag reports whether an If-None-Match header selects the given ETag.
// It accepts a comma-separated list, the `*` wildcard, and treats the weak and
// strong forms of the same value as equal.
func matchesETag(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, part := range strings.Split(header, ",") {
		if strings.TrimPrefix(strings.TrimSpace(part), "W/") == want {
			return true
		}
	}
	return false
}

// sliceMessages returns a tail page of a persisted messages JSON array. When
// `before` is negative the page ends at the array length (newest page);
// otherwise it ends at that index. It returns the encoded page, the total
// message count, the next `before` cursor, and whether older messages remain.
func sliceMessages(raw string, limit, before int) (string, int, int, bool) {
	var messages []json.RawMessage
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &messages)
	}
	if messages == nil {
		messages = []json.RawMessage{}
	}
	total := len(messages)
	end := total
	if before >= 0 && before < total {
		end = before
	}
	start := end - limit
	if start < 0 {
		start = 0
	}
	page, err := json.Marshal(messages[start:end])
	if err != nil {
		page = []byte("[]")
	}
	return string(page), total, start, start > 0
}

// restArchiveSession marks a session as archived so it no longer appears active.
func (s *Server) restArchiveSession(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if sessionID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session id is required"})
		return
	}

	if err := s.store.ArchiveSession(sessionID); err != nil {
		slog.Error("archive session", "session_id", sessionID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"status":     "archived",
	})
}
