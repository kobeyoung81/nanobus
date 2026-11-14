package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// The fleet view: the runners the platform has seen, and the container requests
// it has issued.
//
// Both tables were read straight out of MySQL by the portal — the fleet page, the
// runtime snapshot and the overview's counters. The bus owns them now; what these
// endpoints return is the console's shape, including the two numbers the overview
// prints.

func (s *Server) restListRunners(w http.ResponseWriter, r *http.Request) {
	runners, err := s.lookupRunners(r.Context())
	if err != nil {
		slog.Error("list runners", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	out := make([]map[string]any, 0, len(runners))
	for _, run := range runners {
		out = append(out, map[string]any{
			"id":             run.ID,
			"hostname":       run.Hostname,
			"docker_host":    run.DockerHost,
			"status":         run.Status,
			"last_heartbeat": run.LastHeartbeat.UTC().Format(time.RFC3339),
			"registered_at":  run.RegisteredAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"runners": out})
}

func (s *Server) restCountRunners(w http.ResponseWriter, r *http.Request) {
	online, err := s.lookupOnlineRunners(r.Context())
	if err != nil {
		slog.Error("count online runners", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"online": online})
}

func (s *Server) restListContainerRequests(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	containers, err := s.lookupContainers(r.Context(), userID, limit)
	if err != nil {
		slog.Error("list containers", "user_id", userID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	out := make([]map[string]any, 0, len(containers))
	for _, c := range containers {
		out = append(out, map[string]any{
			"id":         c.ID,
			"user_id":    c.UserID,
			"level":      c.Level,
			"status":     c.Status,
			"runner_id":  c.RunnerID,
			"updated_at": c.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"containers": out})
}

// restCountContainerRequests returns the buckets the console adds up, rather than one
// number per view: "active" is a sum of statuses that the caller decides, so a new
// status does not need a new endpoint.
func (s *Server) restCountContainerRequests(w http.ResponseWriter, r *http.Request) {
	byStatus, err := s.lookupContainerCounts(r.Context())
	if err != nil {
		slog.Error("count containers", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"by_status": byStatus})
}
