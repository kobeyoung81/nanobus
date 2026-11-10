package store

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ErrAgentNotFound reports an update against an agent that does not exist.
var ErrAgentNotFound = errors.New("agent not found")

type AgentRecord struct {
	ID          string
	UserID      string
	TypeID      string
	GroupID     string
	DisplayName string
	Description string
	// ModelID/Thinking/Settings are deprecated: the agent-level model, thinking
	// and runtime settings now live per (agent, type) in agent_type_overrides.
	// They remain as read-only zero values for wire compatibility (AgentInfo).
	ModelID               int64
	Thinking              string
	Settings              map[string]string
	MaxIterations         int
	MaxConcurrentMessages int
	Status                string
}

const (
	agentsCollection        = "portal_agents"
	bindingAgentsCollection = "portal_binding_preferences"
)

// storedSessionAgent is one entry of an agent's switchable session agents.
//
// It is embedded in the agent document rather than kept in its own collection so
// that flipping the default stays a single-document update. The SQL it replaces
// was one statement — `UPDATE agent_session_agents SET is_default = (def_id = ?)
// WHERE agent_id = ?` — and that atomicity is worth keeping: a standalone
// MongoDB has no multi-document transaction to fall back on.
type storedSessionAgent struct {
	DefID     string `bson:"def_id"`
	Position  int    `bson:"position"`
	IsDefault bool   `bson:"is_default"`
}

type storedAgent struct {
	ID      string `bson:"_id"`
	UserID  string `bson:"user_id"`
	TypeID  string `bson:"type_id,omitempty"`
	GroupID string `bson:"group_id,omitempty"`
	// Position is the agent's index inside its group. ArrangeAgents writes
	// 0..n; a freshly created agent gets a millisecond timestamp so it sorts
	// last, and legacy documents have none and fall back to created_at.
	Position              int64                `bson:"position,omitempty"`
	DisplayName           string               `bson:"display_name"`
	Description           string               `bson:"description,omitempty"`
	MaxIterations         int                  `bson:"max_iterations"`
	MaxConcurrentMessages int                  `bson:"max_concurrent_messages"`
	Status                string               `bson:"status"`
	Settings              map[string]string    `bson:"settings,omitempty"`
	SessionAgents         []storedSessionAgent `bson:"session_agents,omitempty"`
	CreatedAt             time.Time            `bson:"created_at"`
	UpdatedAt             time.Time            `bson:"updated_at"`
}

func (d storedAgent) record() AgentRecord {
	return AgentRecord{
		ID: d.ID, UserID: d.UserID, TypeID: d.TypeID, GroupID: d.GroupID,
		DisplayName: d.DisplayName, Description: d.Description,
		MaxIterations: d.MaxIterations, MaxConcurrentMessages: d.MaxConcurrentMessages,
		Status: d.Status, Settings: d.Settings,
	}
}

// maxAgentsForTier is how many agents one account may own. It is a product
// decision rather than an operational dial, so it lives in the binary next to
// the count it bounds; an unknown or empty tier has no ceiling, which is what
// the inline map this replaces did too.
func maxAgentsForTier(tier string) (int, bool) {
	switch tier {
	case "free":
		return 5, true
	case "pro":
		return 10, true
	case "ultra":
		return 40, true
	default:
		return 0, false
	}
}

// sessionAgentsFor is the link set an agent starts with: its own build type
// (default) plus the platform plan type.
func sessionAgentsFor(buildTypeID string) []storedSessionAgent {
	if buildTypeID == "" {
		buildTypeID = "build"
	}
	return []storedSessionAgent{
		{DefID: buildTypeID, Position: 0, IsDefault: true},
		{DefID: "plan", Position: 1, IsDefault: false},
	}
}

// SessionAgent is one session definition linked to an agent.
type SessionAgent struct {
	DefID     string
	Position  int
	IsDefault bool
}

// SessionAgentsForAgent lists an agent's switchable session agents in order.
func (s *Store) SessionAgentsForAgent(agentID string) ([]SessionAgent, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc struct {
		SessionAgents []storedSessionAgent `bson:"session_agents"`
	}
	err := s.db.Collection(agentsCollection).FindOne(ctx, bson.D{{Key: "_id", Value: agentID}},
		options.FindOne().SetProjection(bson.D{{Key: "session_agents", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list session agents: %w", err)
	}

	out := make([]SessionAgent, 0, len(doc.SessionAgents))
	for _, a := range doc.SessionAgents {
		out = append(out, SessionAgent{DefID: a.DefID, Position: a.Position, IsDefault: a.IsDefault})
	}
	// The SQL ordered by position then def_id; the array has no inherent order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].DefID < out[j].DefID
	})
	return out, nil
}

// AgentDisplayNameTaken reports whether another (non-deleted) agent of the same
// user already claims the display name. excludeAgentID skips the agent being
// edited.
//
// MySQL's default collation made this comparison case-insensitive; an exact-match
// query would not be, so the name is compared with a case-insensitive collation
// rather than silently tightening the rule.
func (s *Store) AgentDisplayNameTaken(userID, displayName, excludeAgentID string) (bool, error) {
	ctx, cancel := opCtx()
	defer cancel()

	filter := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "status", Value: bson.D{{Key: "$ne", Value: "deleted"}}},
		{Key: "display_name", Value: displayName},
	}
	if excludeAgentID != "" {
		filter = append(filter, bson.E{Key: "_id", Value: bson.D{{Key: "$ne", Value: excludeAgentID}}})
	}

	// The comparison happens server-side under an explicit collation. MySQL's
	// default collation ("_ai_ci") made this case-insensitive; MongoDB's default
	// is case-sensitive, so matching without saying so would silently tighten the
	// rule. (Comparing in Go would not work at all: a collation only affects
	// server-side comparisons, and the client-side == is always exact.)
	count, err := s.db.Collection(agentsCollection).CountDocuments(ctx, filter,
		options.Count().SetCollation(&options.Collation{Locale: "en", Strength: 2}),
	)
	if err != nil {
		return false, fmt.Errorf("check agent display name: %w", err)
	}
	return count > 0, nil
}

// agentProjection keeps the read shape stable: the deprecated model_id/thinking/
// settings positions are gone from storage (the SQL selected NULL literals for
// them after migration 067 dropped the columns).
var agentProjection = bson.D{
	{Key: "_id", Value: 1},
	{Key: "user_id", Value: 1},
	{Key: "type_id", Value: 1},
	{Key: "group_id", Value: 1},
	{Key: "position", Value: 1},
	{Key: "display_name", Value: 1},
	{Key: "description", Value: 1},
	{Key: "max_iterations", Value: 1},
	{Key: "max_concurrent_messages", Value: 1},
	{Key: "status", Value: 1},
}

func (s *Store) GetAgent(agentID string) (*AgentRecord, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedAgent
	err := s.db.Collection(agentsCollection).FindOne(ctx, bson.D{{Key: "_id", Value: agentID}},
		options.FindOne().SetProjection(agentProjection),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}
	rec := doc.record()
	return &rec, nil
}

func (s *Store) ListAgents(userID string) ([]AgentRecord, error) {
	return s.ListAgentsFiltered(userID, false)
}

// AgentFleetEntry is one row of the runtime snapshot's agent list: every agent on
// the platform, with its owner, newest first.
type AgentFleetEntry struct {
	ID          string
	UserID      string
	DisplayName string
	Status      string
	UpdatedAt   time.Time
}

// ListAgentFleet returns every agent on the platform, most recently updated first.
//
// The console's runtime snapshot is the only caller: it shows the whole fleet, not
// one user's slice, and it needs the owner — which is why this is not
// ListAgentsFiltered with an empty user.
func (s *Store) ListAgentFleet(limit int) ([]AgentFleetEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(agentsCollection).Find(ctx, bson.D{},
		options.Find().
			SetProjection(bson.D{
				{Key: "user_id", Value: 1}, {Key: "display_name", Value: 1},
				{Key: "status", Value: 1}, {Key: "updated_at", Value: 1},
			}).
			SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: 1}}).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("list agent fleet: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []AgentFleetEntry{}
	for cur.Next(ctx) {
		var doc storedAgent
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode agent: %w", err)
		}
		out = append(out, AgentFleetEntry{
			ID: doc.ID, UserID: doc.UserID, DisplayName: doc.DisplayName,
			Status: doc.Status, UpdatedAt: doc.UpdatedAt,
		})
	}
	return out, cur.Err()
}

// ListAgentsFiltered lists a user's agents; when includeDeleted is false
// (default), deleted tombstones are hidden.
func (s *Store) ListAgentsFiltered(userID string, includeDeleted bool) ([]AgentRecord, error) {
	ctx, cancel := opCtx()
	defer cancel()

	filter := bson.D{{Key: "user_id", Value: userID}}
	if !includeDeleted {
		filter = append(filter, bson.E{Key: "status", Value: bson.D{{Key: "$ne", Value: "deleted"}}})
	}

	cur, err := s.db.Collection(agentsCollection).Find(ctx, filter,
		options.Find().SetProjection(agentProjection).SetSort(bson.D{{Key: "created_at", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	agents := []AgentRecord{}
	for cur.Next(ctx) {
		var doc storedAgent
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agents = append(agents, doc.record())
	}
	return agents, cur.Err()
}

// agentIDsForUser returns the ids of a user's agents.
//
// It exists so a caller that needs to scope a query to a user's agents — the
// conversation-attention sidebar, whose sessions live in their own collection —
// can ask here instead of reaching into the agents collection itself.
func (s *Store) agentIDsForUser(userID string) ([]string, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(agentsCollection).Find(ctx,
		bson.D{{Key: "user_id", Value: userID}},
		options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list agent ids: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	var ids []string
	for cur.Next(ctx) {
		var doc struct {
			ID string `bson:"_id"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("scan agent id: %w", err)
		}
		ids = append(ids, doc.ID)
	}
	return ids, cur.Err()
}
