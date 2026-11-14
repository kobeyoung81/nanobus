package server

import (
	"log/slog"
	"net/http"
)

// The prompt blocks of the session agents. The **shared head** is stored on its own
// (session_head_prompts, keyed without a mode, so build and plan cannot diverge);
// the **mode switch notes** still live on the agent types, one prompt each.

// modeNoteIDs are the session types whose prompt is a mode switch note.
var modeNoteIDs = []string{"plan", "build"}

// modeNotes reads the two mode switch notes. They are edited on the agent type
// (the admin module owns that write), so this service only reads them — the user
// plane shows an agent's effective prompt blocks.
func (s *Server) modeNotes() (map[string]string, error) {
	notes := make(map[string]string, len(modeNoteIDs))
	for _, id := range modeNoteIDs {
		t, err := s.store.GetAgentType(id)
		if err != nil {
			return nil, err
		}
		if t != nil {
			notes[id] = t.Prompt
		}
	}
	return notes, nil
}

// restGetAgentPromptBlocks (user plane, read-only) resolves what one agent actually
// runs with: the effective shared head (its own override when one exists, else the
// platform's) and the two mode notes.
func (s *Server) restGetAgentPromptBlocks(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if !s.agentOwnedBy(agentID, userID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}
	if s.catalogue != nil && s.catalogue.Enabled() {
		blocks, err := s.catalogue.PromptBlocks(r.Context(), agentID)
		if err != nil {
			slog.Error("get prompt blocks", "agent_id", agentID, "error", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the catalogue module is unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id":    agentID,
			"head":        blocks.Head,
			"head_source": blocks.HeadSource,
			"mode_notes":  blocks.ModeNotes,
		})
		return
	}
	platform, err := s.store.PlatformSessionHead()
	if err != nil {
		slog.Error("get session head prompt", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	own, err := s.store.AgentSessionHead(agentID)
	if err != nil {
		slog.Error("get agent session head prompt", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	head, source := platform, "platform"
	if own != nil && own.Content != "" {
		head, source = own, "agent"
	}
	notes, err := s.modeNotes()
	if err != nil {
		slog.Error("get mode notes", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id":    agentID,
		"head":        head,
		"head_source": source,
		"mode_notes":  notes,
	})
}
