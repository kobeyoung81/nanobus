package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// restInternalAgentsChanged broadcasts an agents_changed event on behalf of the
// portal, which owns the agent rows.
//
// The supervisors subscribe to this event: create/delete/restore make them
// reconcile their child processes, and restart makes them respawn one. The
// portal cannot publish it itself without holding a second connection to the
// bus's SSE hub, so it asks here.
func (s *Server) restInternalAgentsChanged(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID  string `json:"user_id"`
		Action  string `json:"action"`
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.UserID == "" || body.Action == "" || body.AgentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id, action and agent_id are required"})
		return
	}
	s.emitAgentsChanged(body.UserID, body.Action, body.AgentID)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "broadcast"})
}

// restInternalAgentSettingsChanged broadcasts the config_changed event that
// makes a running process re-read its own record between turns.
func (s *Server) restInternalAgentSettingsChanged(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.AgentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_id is required"})
		return
	}
	s.emitAgentSettingsChanged(body.AgentID)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "broadcast"})
}

// portalCallTimeout bounds a forwarded write. It matches the portal client's own
// timeout so a hung portal surfaces as one error, not two.
const portalCallTimeout = 10 * time.Second

func portalContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), portalCallTimeout)
}
