package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
)

// restGetAgentMode returns one mode's baseline type and the user's override.
func (s *Server) restGetAgentMode(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	typeID := r.PathValue("typeID")
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if !s.agentOwnedBy(agentID, userID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}
	at, err := s.store.GetAgentType(typeID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	ov, err := s.store.GetAgentTypeOverride(agentID, typeID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id": agentID,
		"type_id":  typeID,
		"type":     at,
		"override": ov,
	})
}

// thinkingLevels are the values a client may store as one mode's thinking
// override. The empty string clears it ("follow the platform default").
// "none" is accepted as the spelling the runtime normalizes "off" to; anything
// else would reach the provider as a stray reasoning_effort, so it is refused
// rather than stored and ignored later.
var thinkingLevels = map[string]bool{
	"": true, "off": true, "none": true, "low": true, "medium": true, "high": true,
}

// restSetAgentMode upserts the scalar override (model/thinking/settings) for one
// (agent, mode). Permissions and capabilities have their own endpoints.
func (s *Server) restSetAgentMode(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	typeID := r.PathValue("typeID")
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if !s.agentOwnedBy(agentID, userID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}
	var body struct {
		ModelID      *int64             `json:"model_id"`
		ClearModelID bool               `json:"clear_model_id"`
		Thinking     *string            `json:"thinking"`
		Settings     *map[string]string `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Thinking != nil && !thinkingLevels[*body.Thinking] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported thinking level"})
		return
	}
	// The override document is the admin module's; this service decided whose
	// agent and which mode it is, and forwards the patch. The merge happens
	// there, in one place, against the stored document.
	if err := s.adminPut("/api/v1/internal/agents/"+url.PathEscape(agentID)+"/modes/"+url.PathEscape(typeID),
		map[string]any{
			"model_id":       body.ModelID,
			"clear_model_id": body.ClearModelID,
			"thinking":       body.Thinking,
			"settings":       body.Settings,
		}, nil); err != nil {
		slog.Error("set agent mode override", "error", err)
		writeAdminFailure(w, err)
		return
	}
	s.emitAgentSettingsChanged(agentID)
	s.recordAudit(r, "agent.mode.update", "agent", agentID, map[string]any{"type_id": typeID, "user_id": userID})
	writeJSON(w, http.StatusOK, map[string]string{"agent_id": agentID, "type_id": typeID, "status": "updated"})
}

// agentOwnedBy reports whether the agent exists and belongs to the user.
func (s *Server) agentOwnedBy(agentID, userID string) bool {
	owner, err := s.store.AgentOwner(agentID)
	return err == nil && owner == userID
}
