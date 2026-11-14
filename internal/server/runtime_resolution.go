package server

// Resolving one agent's runtime.
//
// Two sources answer the same question. The admin module owns the catalogue and
// resolves it over its internal API; the bus can also read the collections itself,
// which is what it did before the split and what a rolling upgrade still needs —
// the admin module may be running the previous image.
//
// The agent-side facts the resolution needs (which types one agent runs, and which
// of them is its default) are the row's, and the rows are the portal's. They are
// read here and passed along, so the admin module never has to ask the portal.

import (
	"context"
	"errors"

	"gitee.com/kobeyoung81/nanobus/internal/admincatalogue"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// resolveRuntime answers the snapshot's catalogue half.
func (s *Server) resolveRuntime(agent store.AgentRecord) (*admincatalogue.RuntimeResolution, error) {
	if s.catalogue == nil || !s.catalogue.Enabled() {
		return s.resolveRuntimeLocally(agent)
	}
	sessions, err := s.store.SessionAgentsForAgent(agent.ID)
	if err != nil {
		return nil, err
	}
	types := make([]string, 0, len(sessions))
	for _, sa := range sessions {
		types = append(types, sa.DefID)
	}
	resolution, err := s.catalogue.ResolveRuntime(context.Background(), admincatalogue.RuntimeInput{
		AgentID:      agent.ID,
		UserID:       agent.UserID,
		PrimaryType:  agent.TypeID,
		SessionTypes: types,
		DefaultType:  s.store.DefaultSessionAgent(agent.ID),
	})
	if err != nil {
		// The catalogue module is a hard dependency of a start: falling back here
		// would hide a real outage behind a stale read.
		return nil, err
	}
	return resolution, nil
}

// resolveRuntimeLocally is the path the bus keeps for a rolling upgrade: read the
// collections, resolve in process. It answers exactly what the module answers.
func (s *Server) resolveRuntimeLocally(agent store.AgentRecord) (*admincatalogue.RuntimeResolution, error) {
	caps, err := s.store.ResolveCapabilities(agent.ID)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.EffectiveAgentSettings(agent.ID)
	if err != nil {
		return nil, err
	}
	defs, err := s.store.ResolveAgentDefinitions(agent.ID)
	if err != nil {
		return nil, err
	}
	at, err := s.store.AgentTypeForAgent(agent.ID)
	if err != nil {
		return nil, err
	}
	mode := s.store.DefaultModeTypeID(agent.ID)
	return &admincatalogue.RuntimeResolution{
		DefaultModeTypeID: mode,
		Capabilities:      caps,
		Settings:          settings,
		Definitions:       defs,
		Type:              at,
	}, nil
}

// errAgentNotFound is the sentinel an agent lookup answers with.
var errAgentNotFound = errors.New("agent not found")

// resolveCapabilities answers one agent's default-mode catalogue.
func (s *Server) resolveCapabilities(agentID string) (*nanoprotocol.CapabilitiesResponse, error) {
	if s.catalogue == nil || !s.catalogue.Enabled() {
		return s.store.ResolveCapabilities(agentID)
	}
	agent, err := s.store.GetAgent(agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, errAgentNotFound
	}
	return s.catalogue.ModeCapabilities(context.Background(), agentID, agent.UserID, s.store.DefaultModeTypeID(agentID))
}

// lookupModel and lookupModelProvider answer from the catalogue's owner when it is
// configured. They used to read admin_models and admin_model_providers here.
func (s *Server) lookupModel(id int64) (*nanoprotocol.ModelDef, error) {
	if s.catalogue != nil && s.catalogue.Enabled() {
		return s.catalogue.Model(context.Background(), id)
	}
	return s.store.GetModel(id)
}

func (s *Server) lookupModelProvider(id int64) (*nanoprotocol.ModelProviderDef, error) {
	if s.catalogue != nil && s.catalogue.Enabled() {
		return s.catalogue.ModelProvider(context.Background(), id)
	}
	return s.store.GetModelProvider(id)
}

// lookupAgentMode answers one (agent, mode) from the catalogue's owner. It used to
// be two reads of admin_agent_types and admin_agent_type_overrides.
func (s *Server) lookupAgentMode(agentID, typeID string) (*admincatalogue.AgentMode, error) {
	if s.catalogue != nil && s.catalogue.Enabled() {
		return s.catalogue.AgentMode(context.Background(), agentID, typeID)
	}
	at, err := s.store.GetAgentType(typeID)
	if err != nil {
		return nil, err
	}
	ov, err := s.store.GetAgentTypeOverride(agentID, typeID)
	if err != nil {
		return nil, err
	}
	return &admincatalogue.AgentMode{Type: at, Override: ov}, nil
}
