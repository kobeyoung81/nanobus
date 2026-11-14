package server

import (
	"log/slog"
	"net/http"
)

// The prompt blocks of the session agents. The **shared head** is stored on its own
// (session_head_prompts, keyed without a mode, so build and plan cannot diverge);
// the **mode switch notes** still live on the agent types, one prompt each.

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
	blocks, err := s.lookupPromptBlocks(agentID)
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
}
