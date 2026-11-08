package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func (s *Server) restCreateAgent(w http.ResponseWriter, r *http.Request) {
	var req nanoprotocol.CreateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.UserID == "" || req.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and id are required"})
		return
	}
	if req.DisplayName != "" {
		taken, err := s.store.AgentDisplayNameTaken(req.UserID, req.DisplayName, "")
		if err != nil {
			slog.Error("check agent display name", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		if taken {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "an agent with this name already exists"})
			return
		}
	}

	if req.GroupID != "" {
		ok, err := s.store.AgentGroupExists(req.UserID, req.GroupID)
		if err != nil {
			slog.Error("check agent group", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown group"})
			return
		}
	}

	err := s.store.CreateAgent(store.AgentRecord{
		ID:                    req.ID,
		UserID:                req.UserID,
		TypeID:                s.resolveCreateTypeID(req.TypeID),
		GroupID:               req.GroupID,
		DisplayName:           req.DisplayName,
		Description:           req.Description,
		MaxConcurrentMessages: 4,
	})
	if err != nil {
		slog.Error("create agent", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	container := "pending"
	if err := s.store.EnsureContainerRequest(req.UserID, req.ID); err != nil {
		slog.Error("ensure container request", "user_id", req.UserID, "agent_id", req.ID, "error", err)
		container = "failed"
	}

	s.emitAgentsChanged(req.UserID, "create", req.ID)
	s.recordAudit(r, "agent.create", "agent", req.ID, map[string]any{"user_id": req.UserID})
	writeJSON(w, http.StatusCreated, map[string]any{"id": req.ID, "status": "created", "container": container})
}

func (s *Server) restListAgents(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id query param is required"})
		return
	}

	includeDeleted := r.URL.Query().Get("include_deleted") == "true"
	agents, err := s.store.ListAgentsFiltered(userID, includeDeleted)
	if err != nil {
		slog.Error("list agents", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	result := make([]nanoprotocol.AgentInfo, 0, len(agents))
	for _, a := range agents {
		result = append(result, s.agentInfo(a))
	}

	writeJSON(w, http.StatusOK, map[string]any{"agents": result, "count": len(result)})
}

// agentInfo maps a stored agent record to the wire type, attaching its
// switchable session agents (def ids) and the default session agent.
func (s *Server) agentInfo(a store.AgentRecord) nanoprotocol.AgentInfo {
	var sessionAgents []string
	var defaultAgent string
	if sessions, err := s.store.SessionAgentsForAgent(a.ID); err == nil {
		for _, sa := range sessions {
			sessionAgents = append(sessionAgents, sa.DefID)
			if sa.IsDefault {
				defaultAgent = sa.DefID
			}
		}
		if defaultAgent == "" && len(sessionAgents) > 0 {
			defaultAgent = sessionAgents[0]
		}
	}
	// The effective model is resolved per (agent, mode). Surface the default
	// mode's resolved model so list/detail views (e.g. the account page) can
	// display it without a second round-trip.
	modelID, _ := s.store.DefaultModeModel(a.ID)
	modelName := ""
	if m, err := s.store.GetModel(modelID); err == nil && m != nil {
		modelName = m.Name
	}
	return nanoprotocol.AgentInfo{
		ID:            a.ID,
		UserID:        a.UserID,
		TypeID:        a.TypeID,
		GroupID:       a.GroupID,
		SessionAgents: sessionAgents,
		DefaultAgent:  defaultAgent,
		DisplayName:   a.DisplayName,
		Description:   a.Description,
		Model:         modelName,
		ModelID:       modelID,
		Thinking:      a.Thinking,
		Settings:      a.Settings,
		MaxIterations: a.MaxIterations,
		Status:        a.Status,
		Tools:         []string{},
		Workspaces:    []string{},
	}
}

// resolveCreateTypeID defaults a new agent to the platform conversational type.
func (s *Server) resolveCreateTypeID(requested string) string {
	if requested != "" {
		return requested
	}
	return s.store.DefaultAgentTypeID()
}

func (s *Server) restGetAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	agent, err := s.store.GetAgent(agentID)
	if err != nil {
		slog.Error("get agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	writeJSON(w, http.StatusOK, s.agentInfo(*agent))
}

func (s *Server) restUpdateAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	var req nanoprotocol.UpdateAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.ID = agentID

	existing, err := s.store.GetAgent(agentID)
	if err != nil {
		slog.Error("get agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	if req.DisplayName != nil {
		existing.DisplayName = *req.DisplayName
		if *req.DisplayName != "" {
			taken, err := s.store.AgentDisplayNameTaken(existing.UserID, *req.DisplayName, existing.ID)
			if err != nil {
				slog.Error("check agent display name", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				return
			}
			if taken {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "an agent with this name already exists"})
				return
			}
		}
	}
	if req.Description != nil {
		existing.Description = *req.Description
	}
	if req.TypeID != nil {
		existing.TypeID = *req.TypeID
	}
	if req.DefaultAgent != nil && *req.DefaultAgent != "" {
		if err := s.store.SetDefaultSessionAgent(agentID, *req.DefaultAgent); err != nil {
			slog.Error("set default session agent", "error", err)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	if req.Settings != nil {
		// Documented semantics: present = replace the override map ({} clears).
		if err := s.store.SetAgentSettings(agentID, *req.Settings); err != nil {
			if errors.Is(err, store.ErrUnsupportedAgentSetting) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			slog.Error("set agent settings", "agent", agentID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
	}

	if req.GroupID != nil {
		if *req.GroupID != "" {
			ok, err := s.store.AgentGroupExists(existing.UserID, *req.GroupID)
			if err != nil {
				slog.Error("check agent group", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
				return
			}
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown group"})
				return
			}
		}
		if err := s.store.SetAgentGroup(agentID, *req.GroupID); err != nil {
			slog.Error("set agent group", "agent", agentID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
	}

	if err := s.store.UpdateAgent(*existing); err != nil {
		slog.Error("update agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	s.emitAgentsChanged(existing.UserID, "update", agentID)
	s.emitAgentSettingsChanged(agentID)
	s.recordAudit(r, "agent.update", "agent", agentID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": agentID, "status": "updated"})
}

func (s *Server) restDeleteAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")

	existing, err := s.store.GetAgent(agentID)
	if err != nil {
		slog.Error("get agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if existing == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	if err := s.store.DeleteAgent(agentID); err != nil {
		slog.Error("delete agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	s.emitAgentsChanged(existing.UserID, "delete", agentID)
	s.recordAudit(r, "agent.delete", "agent", agentID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": agentID, "status": "deleted"})
}

// restUpdateAgentStatus records a runtime status report for an agent.
func (s *Server) restUpdateAgentStatus(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	var req nanoprotocol.UpdateAgentStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	req.AgentID = agentID
	if req.Status == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status is required"})
		return
	}

	if err := s.store.SetAgentStatus(req.AgentID, req.Status); err != nil {
		slog.Error("update agent status", "agent_id", agentID, "status", req.Status, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": agentID, "status": req.Status})
}

// restRestartAgent notifies the supervisor to restart a running agent's
// process (re-reads its record + config).
func (s *Server) restRestartAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")

	agent, err := s.store.GetAgent(agentID)
	if err != nil {
		slog.Error("get agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	s.emitAgentsChanged(agent.UserID, "restart", agentID)
	s.recordAudit(r, "agent.restart", "agent", agentID, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": agentID, "status": "restarting"})
}

// restProvisionAgent re-triggers provisioning for a failed agent: resets its
// status to provisioning, ensures a container request exists, and notifies the
// supervisor via an agents_changed event.
func (s *Server) restProvisionAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")

	agent, err := s.store.GetAgent(agentID)
	if err != nil {
		slog.Error("get agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	if agent.Status == "deleted" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "agent is deleted; restore it to start it again"})
		return
	}

	if err := s.store.SetAgentStatus(agentID, "provisioning"); err != nil {
		slog.Error("set agent status", "agent_id", agentID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := s.store.EnsureContainerRequest(agent.UserID, agentID); err != nil {
		slog.Warn("ensure container request on retry", "user_id", agent.UserID, "agent_id", agentID, "error", err)
	}

	s.emitAgentsChanged(agent.UserID, "provision", agentID)
	s.recordAudit(r, "agent.provision", "agent", agentID, nil)
	writeJSON(w, http.StatusAccepted, map[string]string{"id": agentID, "status": "provisioning"})
}

// restRestoreAgent brings a deleted agent back to provisioning and notifies the
// supervisor so it starts again.
func (s *Server) restRestoreAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")

	agent, err := s.store.GetAgent(agentID)
	if err != nil {
		slog.Error("get agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}

	if err := s.store.RestoreAgent(agentID); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err := s.store.EnsureContainerRequest(agent.UserID, agentID); err != nil {
		slog.Warn("ensure container request on restore", "user_id", agent.UserID, "agent_id", agentID, "error", err)
	}

	s.emitAgentsChanged(agent.UserID, "restore", agentID)
	s.recordAudit(r, "agent.restore", "agent", agentID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": agentID, "status": "provisioning"})
}
