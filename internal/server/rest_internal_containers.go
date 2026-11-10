package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
)

// restInternalContainerRequest enqueues a pending provisioning request.
//
// The portal owns the agent rows now, so creating or restarting an agent happens
// there; the queue and the poll that serves it are still this service's. The
// portal therefore asks here rather than writing the queue's collection itself —
// one writer, one queue.
func (s *Server) restInternalContainerRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID  string `json:"user_id"`
		AgentID string `json:"agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.UserID == "" || body.AgentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and agent_id are required"})
		return
	}

	if err := s.callProvisioner(r, func(ctx context.Context, c *provisionerclient.Client) error {
		return c.EnqueueForAgent(ctx, body.UserID, body.AgentID)
	}); err != nil {
		slog.Error("enqueue container request", "user_id", body.UserID, "agent_id", body.AgentID, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "provisioner unavailable"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}
