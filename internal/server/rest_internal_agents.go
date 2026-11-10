package server

import (
	"log/slog"
	"net/http"
)

// restClearAgentTypeRefs answers the admin module's "I am about to delete this
// type" call by clearing the reference on the agents that point at it.
//
// The catalogue and the agent rows belong to different services now. Deleting a
// type without this would leave agents pointing at a definition nobody can
// resolve — they would fall back to no type at all, silently.
func (s *Server) restClearAgentTypeRefs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "type id is required"})
		return
	}
	// The agent rows belong to the portal, so the reference is cleared there.
	if s.portal == nil || !s.portal.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "the portal module is not configured"})
		return
	}
	ctx, cancel := portalContext()
	defer cancel()
	cleared, err := s.portal.ClearTypeRefs(ctx, id)
	if err != nil {
		slog.Error("clear agent type references", "type_id", id, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"type_id": id, "cleared": cleared})
}
