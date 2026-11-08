package server

import (
	"log/slog"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// handleCreateAgent creates a per-user agent over the WebSocket protocol and
// ensures a container provisioning request is enqueued for the owner.
func (s *Server) handleCreateAgent(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.CreateAgentRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.UserID == "" || req.ID == "" {
		resp := nanoprotocol.NewErrorMessage("user_id and id are required")
		return &resp
	}

	if req.GroupID != "" {
		ok, err := s.store.AgentGroupExists(req.UserID, req.GroupID)
		if err != nil {
			slog.Error("check agent group", "error", err)
			resp := nanoprotocol.NewErrorMessage("check agent group: " + err.Error())
			return &resp
		}
		if !ok {
			resp := nanoprotocol.NewErrorMessage("unknown group")
			return &resp
		}
	}

	if err := s.store.CreateAgent(store.AgentRecord{
		ID:                    req.ID,
		UserID:                req.UserID,
		TypeID:                s.resolveCreateTypeID(req.TypeID),
		GroupID:               req.GroupID,
		DisplayName:           req.DisplayName,
		Description:           req.Description,
		MaxConcurrentMessages: 4,
	}); err != nil {
		slog.Error("create agent", "agent_id", req.ID, "error", err)
		resp := nanoprotocol.NewErrorMessage("create agent: " + err.Error())
		return &resp
	}

	if err := s.store.EnsureContainerRequest(req.UserID, req.ID); err != nil {
		slog.Error("ensure container request", "user_id", req.UserID, "agent_id", req.ID, "error", err)
	}

	s.emitAgentsChanged(req.UserID, "create", req.ID)

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeAgentCreated, map[string]string{"id": req.ID, "status": "created"})
	return &resp
}

// handleUpdateAgent applies a partial update to an existing agent over the
// WebSocket protocol.
func (s *Server) handleUpdateAgent(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.UpdateAgentRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.ID == "" {
		resp := nanoprotocol.NewErrorMessage("id is required")
		return &resp
	}

	existing, err := s.store.GetAgent(req.ID)
	if err != nil {
		slog.Error("get agent", "agent_id", req.ID, "error", err)
		resp := nanoprotocol.NewErrorMessage("get agent: " + err.Error())
		return &resp
	}
	if existing == nil {
		resp := nanoprotocol.NewErrorMessage("agent not found")
		return &resp
	}

	if req.DisplayName != nil {
		existing.DisplayName = *req.DisplayName
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.DefaultAgent != nil && *req.DefaultAgent != "" {
		if err := s.store.SetDefaultSessionAgent(req.ID, *req.DefaultAgent); err != nil {
			resp := nanoprotocol.NewErrorMessage("set default session agent: " + err.Error())
			return &resp
		}
	}

	if req.GroupID != nil {
		if *req.GroupID != "" {
			ok, err := s.store.AgentGroupExists(existing.UserID, *req.GroupID)
			if err != nil {
				slog.Error("check agent group", "error", err)
				resp := nanoprotocol.NewErrorMessage("check agent group: " + err.Error())
				return &resp
			}
			if !ok {
				resp := nanoprotocol.NewErrorMessage("unknown group")
				return &resp
			}
		}
		if err := s.store.SetAgentGroup(req.ID, *req.GroupID); err != nil {
			slog.Error("set agent group", "agent_id", req.ID, "error", err)
			resp := nanoprotocol.NewErrorMessage("set agent group: " + err.Error())
			return &resp
		}
	}

	if err := s.store.UpdateAgent(*existing); err != nil {
		slog.Error("update agent", "agent_id", req.ID, "error", err)
		resp := nanoprotocol.NewErrorMessage("update agent: " + err.Error())
		return &resp
	}

	s.emitAgentsChanged(existing.UserID, "update", req.ID)

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeAgentUpdated, map[string]string{"id": req.ID, "status": "updated"})
	return &resp
}

// handleDeleteAgent soft-deletes an agent over the WebSocket protocol.
func (s *Server) handleDeleteAgent(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.DeleteAgentRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.ID == "" {
		resp := nanoprotocol.NewErrorMessage("id is required")
		return &resp
	}

	existing, err := s.store.GetAgent(req.ID)
	if err != nil {
		slog.Error("get agent", "agent_id", req.ID, "error", err)
		resp := nanoprotocol.NewErrorMessage("get agent: " + err.Error())
		return &resp
	}
	if existing == nil {
		resp := nanoprotocol.NewErrorMessage("agent not found")
		return &resp
	}

	if err := s.store.DeleteAgent(req.ID); err != nil {
		slog.Error("delete agent", "agent_id", req.ID, "error", err)
		resp := nanoprotocol.NewErrorMessage("delete agent: " + err.Error())
		return &resp
	}

	s.emitAgentsChanged(existing.UserID, "delete", req.ID)

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeAgentDeleted, nanoprotocol.DeleteAgentResponse{ID: req.ID})
	return &resp
}

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

	if err := s.store.SetAgentStatus(req.AgentID, req.Status); err != nil {
		slog.Error("update agent status", "agent_id", req.AgentID, "status", req.Status, "error", err)
		resp := nanoprotocol.NewErrorMessage("update agent status: " + err.Error())
		return &resp
	}

	slog.Info("agent status updated", "agent_id", req.AgentID, "status", req.Status)
	resp := nanoprotocol.Message{Type: "ok"}
	return &resp
}
