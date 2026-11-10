package server

import (
	"context"
	"log/slog"
	"time"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// handleUpdateAgentStatus records a runtime status report for an agent (from
// the hypervisor supervising it).
func (s *Server) handleUpdateAgentStatus(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.UpdateAgentStatusRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.AgentID == "" || req.Status == "" {
		resp := nanoprotocol.NewErrorMessage("agent_id and status are required")
		return &resp
	}

	// The agent rows are the portal's now, so the write goes there. The
	// supervisor keeps reporting through the protocol message it has always
	// used; this service is only the address it knows.
	if s.portal == nil || !s.portal.Enabled() {
		resp := nanoprotocol.NewErrorMessage("agent status cannot be recorded: the portal module is not configured")
		return &resp
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.portal.SetAgentStatus(ctx, req.AgentID, req.Status); err != nil {
		slog.Error("update agent status", "agent_id", req.AgentID, "status", req.Status, "error", err)
		resp := nanoprotocol.NewErrorMessage("update agent status: " + err.Error())
		return &resp
	}

	slog.Info("agent status updated", "agent_id", req.AgentID, "status", req.Status)
	resp := nanoprotocol.Message{Type: "ok"}
	return &resp
}
