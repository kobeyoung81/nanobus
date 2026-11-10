package server

import (
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// agentInfo maps a stored agent record to the wire type, attaching its
// switchable session agents (def ids) and the default session agent.
//
// The portal owns the agent rows and serves the management surface; this shape
// survives because the agents' supervisors and the gateway list their agents
// over the protocol, and both read the same documents from the same database.
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
	// mode's resolved model so list/detail views can display it without a second
	// round-trip.
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
