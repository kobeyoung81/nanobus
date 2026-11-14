package server

import (
	"gitee.com/kobeyoung81/nanobus/internal/portalagents"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// agentInfo maps an agent row to the wire type, attaching its switchable session
// modes and the default one.
//
// The portal owns the rows and serves the management surface; this shape survives
// because the agents' supervisors and the gateway list their agents over the
// protocol. The row already carries its session modes (the portal answers them),
// so nothing is resolved twice here.
func (s *Server) agentInfo(a portalagents.Agent) nanoprotocol.AgentInfo {
	sessionAgents := a.SessionAgents
	defaultAgent := a.DefaultAgent
	if defaultAgent == "" && len(sessionAgents) > 0 {
		defaultAgent = sessionAgents[0]
	}
	// The effective model is resolved per (agent, mode). Surface the default
	// mode's resolved model so list/detail views can display it without a second
	// round-trip.
	modelID, modelName := s.lookupDefaultModeModel(a)
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
