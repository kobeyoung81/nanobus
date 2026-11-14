package server

// Asking each domain for its own data.
//
// Every placement of a domain moved twice: first the rows, then the reads. These
// are the lookups the handlers call, and each one asks the module that owns the
// rows:
//
//   - the agent rows, their session modes and their groups are the portal's;
//   - the catalogue (types, overrides, models, prompts, capabilities) is the admin
//     module's;
//   - the runner fleet and the container queue are the provisioner's;
//   - messages, stream events and the tunnels are the bus's own.
//
// Nothing here reads another module's collection, and there is no fallback: an
// unconfigured owner fails the call instead of answering from a stale local read,
// which would turn an outage into wrong data.

import (
	"context"
	"errors"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/admincatalogue"
	"gitee.com/kobeyoung81/nanobus/internal/portalagents"
	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// errAgentNotFound is the sentinel an agent lookup answers with.
var errAgentNotFound = errors.New("agent not found")

// defaultAgentTypeID is the platform's own type, used when an agent row names
// neither a default session mode nor a primary type.
const defaultAgentTypeID = "build"

// lookupAgent asks the agent rows' owner for one row.
func (s *Server) lookupAgent(agentID string) (*portalagents.Agent, error) {
	agent, err := s.agentSource.GetAgent(context.Background(), agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, errAgentNotFound
	}
	return agent, nil
}

// defaultModeTypeID is the mode a turn starts in: the agent's default session
// mode, else its primary type, else the platform default.
func defaultModeTypeID(a portalagents.Agent) string {
	if a.DefaultAgent != "" {
		return a.DefaultAgent
	}
	if a.TypeID != "" {
		return a.TypeID
	}
	return defaultAgentTypeID
}

// resolveRuntime answers the snapshot's catalogue half.
func (s *Server) resolveRuntime(agent portalagents.Agent) (*admincatalogue.RuntimeResolution, error) {
	return s.catalogue.ResolveRuntime(context.Background(), admincatalogue.RuntimeInput{
		AgentID:      agent.ID,
		UserID:       agent.UserID,
		PrimaryType:  agent.TypeID,
		SessionTypes: agent.SessionAgents,
		DefaultType:  agent.DefaultAgent,
	})
}

// resolveCapabilities answers one agent's default-mode catalogue.
func (s *Server) resolveCapabilities(agentID string) (*nanoprotocol.CapabilitiesResponse, error) {
	agent, err := s.lookupAgent(agentID)
	if err != nil {
		return nil, err
	}
	return s.catalogue.ModeCapabilities(context.Background(), agentID, agent.UserID, defaultModeTypeID(*agent))
}

// resolveModeCapabilities answers one (agent, mode) catalogue snapshot.
func (s *Server) resolveModeCapabilities(agentID, userID, typeID string) (*nanoprotocol.CapabilitiesResponse, error) {
	return s.catalogue.ModeCapabilities(context.Background(), agentID, userID, typeID)
}

// lookupModel and lookupModelProvider answer from the catalogue's owner.
func (s *Server) lookupModel(id int64) (*nanoprotocol.ModelDef, error) {
	return s.catalogue.Model(context.Background(), id)
}

func (s *Server) lookupModelProvider(id int64) (*nanoprotocol.ModelProviderDef, error) {
	return s.catalogue.ModelProvider(context.Background(), id)
}

// lookupAgentMode answers one (agent, mode) as the per-agent mode editor reads it.
func (s *Server) lookupAgentMode(agentID, typeID string) (*admincatalogue.AgentMode, error) {
	return s.catalogue.AgentMode(context.Background(), agentID, typeID)
}

// lookupAgentTypes answers the type catalogue.
func (s *Server) lookupAgentTypes(sessionOnly bool) ([]nanoprotocol.AgentTypeDef, error) {
	return s.catalogue.AgentTypes(context.Background(), sessionOnly)
}

// lookupPromptBlocks answers what one agent runs with: the effective shared head
// and the two mode switch notes.
func (s *Server) lookupPromptBlocks(agentID string) (*admincatalogue.PromptBlocks, error) {
	return s.catalogue.PromptBlocks(context.Background(), agentID)
}

// capabilityVisible reports whether a capability is visible to a user. The
// catalogues are the admin module's, and this one is on a write path.
func (s *Server) capabilityVisible(kind, ref, userID string) (bool, error) {
	return s.catalogue.CapabilityVisible(context.Background(), kind, ref, userID)
}

// lookupDefaultModeModel answers the model one agent's default mode runs with,
// which the agent list shows next to every row.
func (s *Server) lookupDefaultModeModel(agent portalagents.Agent) (int64, string) {
	modelID, name, err := s.catalogue.DefaultModel(context.Background(), admincatalogue.RuntimeInput{
		AgentID:     agent.ID,
		UserID:      agent.UserID,
		PrimaryType: agent.TypeID,
		DefaultType: agent.DefaultAgent,
	})
	if err != nil {
		return 0, ""
	}
	return modelID, name
}

// AgentFleetRow is one row of the console's fleet list.
type AgentFleetRow struct {
	ID          string
	UserID      string
	DisplayName string
	Status      string
	UpdatedAt   time.Time
}

// lookupAgentFleet answers every agent on the platform, newest first.
func (s *Server) lookupAgentFleet(ctx context.Context, limit int) ([]AgentFleetRow, error) {
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

// The runner fleet and the container queue, answered by the provisioner.

func (s *Server) lookupRunners(ctx context.Context) ([]provisionerclient.RunnerSummary, error) {
	return s.provisioner.Runners(ctx)
}

func (s *Server) lookupOnlineRunners(ctx context.Context) (int, error) {
	return s.provisioner.OnlineRunners(ctx)
}

func (s *Server) lookupContainers(ctx context.Context, userID string, limit int) ([]provisionerclient.ContainerSummary, error) {
	return s.provisioner.Containers(ctx, userID, limit)
}

func (s *Server) lookupContainerCounts(ctx context.Context) (map[string]int, error) {
	return s.provisioner.ContainerCounts(ctx)
}

// lookupContainerRequests answers one user's container requests in the protocol's
// shape: the queue's owner returns rows, and the wire wants the request.
func (s *Server) lookupContainerRequests(ctx context.Context, userID string, limit int) ([]nanoprotocol.ContainerRequest, error) {
	rows, err := s.lookupContainers(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]nanoprotocol.ContainerRequest, 0, len(rows))
	for _, row := range rows {
		out = append(out, nanoprotocol.ContainerRequest{
			UserID:    row.UserID,
			BindingID: row.BindingID,
			AgentID:   row.AgentID,
			Level:     row.Level,
			Status:    row.Status,
		})
	}
	return out, nil
}
