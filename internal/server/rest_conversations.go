package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// The conversation log's two reads. They exist as their own endpoints because the
// portal used to read `agent_sessions` straight out of MySQL: that made one
// table's schema a contract between two services, and it is exactly what made
// moving the storage a change in two repositories. Going through the bus also
// keeps a read of somebody else's store behind one owner — the bus.
//
// The shape is the portal's model, not the bus's session shape: the point of the
// endpoint is that the caller does not know how sessions are stored.

// searchTermLimit bounds one free-text condition. A fragment long enough to matter
// is far shorter than this; the cap keeps a pasted document from becoming a
// pattern.
const searchTermLimit = 128

// searchTerm reads one filter parameter: trimmed, and capped in runes so a
// multi-byte fragment is never cut mid-character.
func searchTerm(r *http.Request, key string) string {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if runes := []rune(value); len(runes) > searchTermLimit {
		return strings.TrimSpace(string(runes[:searchTermLimit]))
	}
	return value
}

// conversationJSON is one row of the log.
func conversationJSON(rec *store.ConversationEntry) map[string]any {
	return map[string]any{
		"id":             rec.ID,
		"agent_id":       rec.AgentID,
		"agent_name":     rec.AgentName,
		"account_id":     rec.AccountID,
		"account_name":   rec.AccountName,
		"channel":        rec.Channel,
		"chat_id":        rec.ChatID,
		"status":         rec.Status,
		"message_count":  rec.MessageCount,
		"messages_bytes": rec.MessagesBytes,
		"updated_at":     rec.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (s *Server) restListConversations(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	records, err := s.store.ListConversations(store.ConversationFilter{
		Limit:   limit,
		Session: searchTerm(r, "session"),
		Agent:   searchTerm(r, "agent"),
		Account: searchTerm(r, "account"),
		Channel: searchTerm(r, "channel"),
		Status:  searchTerm(r, "status"),
	})
	if err != nil {
		slog.Error("list conversations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]map[string]any, 0, len(records))
	for i := range records {
		out = append(out, conversationJSON(&records[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"conversations": out})
}

func (s *Server) restGetConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionID")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "session id is required"})
		return
	}

	rec, err := s.store.GetSession(id)
	if err != nil {
		slog.Error("get conversation", "session_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "conversation not found"})
		return
	}

	entry := store.ConversationEntry{SessionRecord: *rec}
	body := conversationJSON(&entry)
	body["summary"] = rec.Summary
	// The stored conversation is a JSON array already; it is passed through as
	// JSON rather than re-encoded through a map, so the caller gets the messages
	// exactly as they were stored.
	body["messages"] = json.RawMessage(rec.Messages)
	writeJSON(w, http.StatusOK, body)
}
