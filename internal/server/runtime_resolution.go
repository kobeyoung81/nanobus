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
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/admincatalogue"
	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
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

// The console's fleet and queue reads. Each owner answers them now; the local read
// stays behind the same kind of switch as the catalogue, so a rolling upgrade keeps
// working while the other module still runs the previous image.

// AgentFleetRow is one row of the console's fleet list.
type AgentFleetRow struct {
	ID          string
	UserID      string
	DisplayName string
	Status      string
	UpdatedAt   time.Time
}

func (s *Server) lookupAgentFleet(ctx context.Context, limit int) ([]AgentFleetRow, error) {
	if s.agentSource == nil || !s.agentSource.Enabled() {
		rows, err := s.store.ListAgentFleet(limit)
		if err != nil {
			return nil, err
		}
		out := make([]AgentFleetRow, 0, len(rows))
		for _, row := range rows {
			out = append(out, AgentFleetRow{ID: row.ID, UserID: row.UserID, DisplayName: row.DisplayName, Status: row.Status, UpdatedAt: row.UpdatedAt})
		}
		return out, nil
	}
	rows, err := s.agentSource.Fleet(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]AgentFleetRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, AgentFleetRow{ID: row.ID, UserID: row.UserID, DisplayName: row.DisplayName, Status: row.Status, UpdatedAt: row.UpdatedAt})
	}
	return out, nil
}

func (s *Server) lookupRunners(ctx context.Context) ([]provisionerclient.RunnerSummary, error) {
	if s.provisioner != nil && s.provisioner.Enabled() {
		return s.provisioner.Runners(ctx)
	}
	rows, err := s.store.ListRunners()
	if err != nil {
		return nil, err
	}
	out := make([]provisionerclient.RunnerSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, provisionerclient.RunnerSummary{
			ID: row.ID, Hostname: row.Hostname, DockerHost: row.DockerHost,
			Status: row.Status, LastHeartbeat: row.LastHeartbeat, RegisteredAt: row.RegisteredAt,
		})
	}
	return out, nil
}

func (s *Server) lookupOnlineRunners(ctx context.Context) (int, error) {
	if s.provisioner == nil || !s.provisioner.Enabled() {
		return s.store.CountOnlineRunners()
	}
	return s.provisioner.OnlineRunners(ctx)
}

func (s *Server) lookupContainers(ctx context.Context, userID string, limit int) ([]store.ContainerSummary, error) {
	if s.provisioner == nil || !s.provisioner.Enabled() {
		return s.store.ListContainerSummaries(userID, limit)
	}
	rows, err := s.provisioner.Containers(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]store.ContainerSummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.ContainerSummary{
			ID: row.ID, UserID: row.UserID, Level: row.Level, Status: row.Status,
			RunnerID: row.RunnerID, UpdatedAt: row.UpdatedAt,
		})
	}
	return out, nil
}

func (s *Server) lookupContainerCounts(ctx context.Context) (map[string]int, error) {
	if s.provisioner == nil || !s.provisioner.Enabled() {
		return s.store.CountContainersByStatus()
	}
	return s.provisioner.ContainerCounts(ctx)
}

// lookupAgentTypes answers the type catalogue. It used to be a read of
// admin_agent_types here.
func (s *Server) lookupAgentTypes(sessionOnly bool) ([]nanoprotocol.AgentTypeDef, error) {
	if s.catalogue != nil && s.catalogue.Enabled() {
		return s.catalogue.AgentTypes(context.Background(), sessionOnly)
	}
	return s.store.ListAgentTypes(sessionOnly)
}

// resolveModeCapabilities answers one (agent, mode) catalogue snapshot.
func (s *Server) resolveModeCapabilities(agentID, userID, typeID string) (*nanoprotocol.CapabilitiesResponse, error) {
	if s.catalogue != nil && s.catalogue.Enabled() {
		return s.catalogue.ModeCapabilities(context.Background(), agentID, userID, typeID)
	}
	return s.store.ResolveModeCapabilities(agentID, typeID)
}

// capabilityVisible reports whether a capability is visible to a user. The
// catalogues are the admin module's; until it answers this question over its API,
// the bus reads them — the one foreign read left on a write path.
func (s *Server) capabilityVisible(kind, ref, userID string) (bool, error) {
	return s.store.CapabilityVisible(kind, ref, userID)
}

// lookupDefaultModeModel answers the model one agent's default mode runs with.
func (s *Server) lookupDefaultModeModel(agentID string) (int64, string) {
	return s.store.DefaultModeModel(agentID)
}

// localPromptBlocks is the bus's own answer to the user-facing prompt-block view,
// kept for the rolling upgrade: the platform head, the agent's own head, and the
// two mode notes.
func (s *Server) localPromptBlocks(agentID string) (head, source string, notes map[string]string, err error) {
	platform, err := s.store.PlatformSessionHead()
	if err != nil {
		return "", "", nil, err
	}
	own, err := s.store.AgentSessionHead(agentID)
	if err != nil {
		return "", "", nil, err
	}
	head, source = platform.Content, "platform"
	if own != nil && own.Content != "" {
		head, source = own.Content, "agent"
	}
	notes = make(map[string]string, len(modeNoteIDs))
	for _, id := range modeNoteIDs {
		t, err := s.store.GetAgentType(id)
		if err != nil {
			return "", "", nil, err
		}
		if t != nil {
			notes[id] = t.Prompt
		}
	}
	return head, source, notes, nil
}
