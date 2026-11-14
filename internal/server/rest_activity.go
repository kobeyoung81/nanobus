package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// The runtime snapshot's agent list and the user list's "last seen" column. Both
// were SELECTs against MySQL — `agents` and the two queues — and both are the
// bus's now.

// restListAgentFleet serves every agent on the platform, newest first, with its
// owner: the snapshot shows the whole fleet, not one user's slice.
func (s *Server) restListAgentFleet(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	agents, err := s.lookupAgentFleet(r.Context(), limit)
	if err != nil {
		slog.Error("list agent fleet", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	out := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		out = append(out, map[string]any{
			"id":           a.ID,
			"user_id":      a.UserID,
			"display_name": a.DisplayName,
			"status":       a.Status,
			"updated_at":   a.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out, "count": len(out)})
}

// restUserActivity serves "last seen" per user: the newest queued message in
// either direction. The console's user list shows it, and only the queues know it.
func (s *Server) restUserActivity(w http.ResponseWriter, r *http.Request) {
	activity, err := s.store.LastActivityByUser()
	if err != nil {
		slog.Error("last activity", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make(map[string]string, len(activity))
	for userID, at := range activity {
		out[userID] = at.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, map[string]any{"activity": out})
}
