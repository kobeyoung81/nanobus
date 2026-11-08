package store

import (
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// agentTypeOverridesCollection holds the user plane of the type resolution: what
// one agent has changed about one mode.
const agentTypeOverridesCollection = "agent_type_overrides"

// storedCapabilityAttachment is one capability as the user set it on a mode.
type storedCapabilityAttachment struct {
	Kind    nanoprotocol.CapabilityKind `bson:"kind"`
	Ref     string                      `bson:"ref"`
	Enabled bool                        `bson:"enabled"`
}

// storedAgentTypeOverride is the document shape.
//
// _id is an ObjectID and the (agent_id, type_id) pair — the SQL primary key —
// is a unique index instead: building an id out of two strings is how a
// separator that appears in one of them silently collides, the same reason the
// config domain did not concatenate either.
type storedAgentTypeOverride struct {
	ID           bson.ObjectID                `bson:"_id,omitempty"`
	AgentID      string                       `bson:"agent_id"`
	TypeID       string                       `bson:"type_id"`
	ModelID      *int64                       `bson:"model_id,omitempty"`
	Thinking     string                       `bson:"thinking,omitempty"`
	Settings     map[string]string            `bson:"settings,omitempty"`
	Permissions  []storedPermissionRule       `bson:"permissions,omitempty"`
	Capabilities []storedCapabilityAttachment `bson:"capabilities,omitempty"`
	UpdatedAt    time.Time                    `bson:"updated_at"`
}

func (d storedAgentTypeOverride) override() *nanoprotocol.AgentTypeOverride {
	ov := &nanoprotocol.AgentTypeOverride{Thinking: d.Thinking, Settings: d.Settings}
	if d.ModelID != nil {
		ov.ModelID = *d.ModelID
	}
	if len(d.Permissions) > 0 {
		ov.Permissions = make([]nanoprotocol.PermissionRule, 0, len(d.Permissions))
		for _, r := range d.Permissions {
			ov.Permissions = append(ov.Permissions, nanoprotocol.PermissionRule{Pattern: r.Pattern, Action: r.Action})
		}
	}
	if len(d.Capabilities) > 0 {
		ov.Capabilities = make([]nanoprotocol.CapabilityAttachment, 0, len(d.Capabilities))
		for _, c := range d.Capabilities {
			ov.Capabilities = append(ov.Capabilities, nanoprotocol.CapabilityAttachment{
				Kind: c.Kind, Ref: c.Ref, Enabled: c.Enabled,
			})
		}
	}
	return ov
}

// overrideIdentity is the pair the SQL's primary key covered.
func overrideIdentity(agentID, typeID string) bson.D {
	return bson.D{
		{Key: "agent_id", Value: agentID},
		{Key: "type_id", Value: typeID},
	}
}

// GetAgentTypeOverride returns the user's override for one (agent, type) pair,
// or nil when none is stored.
func (s *Store) GetAgentTypeOverride(agentID, typeID string) (*nanoprotocol.AgentTypeOverride, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedAgentTypeOverride
	err := s.db.Collection(agentTypeOverridesCollection).
		FindOne(ctx, overrideIdentity(agentID, typeID)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent type override: %w", err)
	}
	return doc.override(), nil
}

// ListAgentTypeOverrides returns every override for an agent keyed by type id.
func (s *Store) ListAgentTypeOverrides(agentID string) (map[string]nanoprotocol.AgentTypeOverride, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(agentTypeOverridesCollection).
		Find(ctx, bson.D{{Key: "agent_id", Value: agentID}})
	if err != nil {
		return nil, fmt.Errorf("list agent type overrides: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := map[string]nanoprotocol.AgentTypeOverride{}
	for cur.Next(ctx) {
		var doc storedAgentTypeOverride
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode agent type override: %w", err)
		}
		out[doc.TypeID] = *doc.override()
	}
	return out, cur.Err()
}

// SetAgentTypeOverride upserts the override for one (agent, type) pair. The
// permission rules are validated against the read-only key catalog.
func (s *Store) SetAgentTypeOverride(agentID, typeID string, ov nanoprotocol.AgentTypeOverride) error {
	if agentID == "" || typeID == "" {
		return fmt.Errorf("agent_id and type_id are required")
	}
	if len(ov.Permissions) > 0 {
		if err := s.validateReadOnlyRules(ov.Permissions); err != nil {
			return err
		}
	}

	ctx, cancel := opCtx()
	defer cancel()

	// Every field is written, empty ones included: the SQL's upsert took the
	// values from the statement, so an override with no settings *cleared* the
	// stored ones rather than leaving them.
	set := bson.D{
		{Key: "thinking", Value: ov.Thinking},
		{Key: "settings", Value: ov.Settings},
		{Key: "permissions", Value: storedPermissionRules(ov.Permissions)},
		{Key: "capabilities", Value: storedCapabilityAttachments(ov.Capabilities)},
		{Key: "updated_at", Value: time.Now().UTC()},
	}
	var update bson.D
	if ov.ModelID == 0 {
		// A zero model id was NULLIF(?, 0) — absent, not a model 0 that nothing
		// would resolve.
		update = bson.D{
			{Key: "$set", Value: set},
			{Key: "$unset", Value: bson.D{{Key: "model_id", Value: ""}}},
		}
	} else {
		update = bson.D{{Key: "$set", Value: append(set,
			bson.E{Key: "model_id", Value: ov.ModelID})}}
	}

	err := retryOnDuplicateUpsert(func() error {
		_, err := s.db.Collection(agentTypeOverridesCollection).UpdateOne(ctx,
			overrideIdentity(agentID, typeID), update, options.UpdateOne().SetUpsert(true))
		return err
	})
	if err != nil {
		return fmt.Errorf("set agent type override: %w", err)
	}
	return nil
}

func storedCapabilityAttachments(caps []nanoprotocol.CapabilityAttachment) []storedCapabilityAttachment {
	if len(caps) == 0 {
		return nil
	}
	out := make([]storedCapabilityAttachment, 0, len(caps))
	for _, c := range caps {
		out = append(out, storedCapabilityAttachment{Kind: c.Kind, Ref: c.Ref, Enabled: c.Enabled})
	}
	return out
}

// DeleteAgentTypeOverride clears one (agent, type) override.
func (s *Store) DeleteAgentTypeOverride(agentID, typeID string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if _, err := s.db.Collection(agentTypeOverridesCollection).
		DeleteOne(ctx, overrideIdentity(agentID, typeID)); err != nil {
		return fmt.Errorf("delete agent type override: %w", err)
	}
	return nil
}

// EffectiveTypeSettings resolves the runtime settings for one (agent, type):
// the AgentType baseline overlaid by the user override (override wins). Empty
// values are dropped so the runtime falls back to its code defaults.
func (s *Store) EffectiveTypeSettings(agentID, typeID string) (map[string]string, error) {
	out := map[string]string{}
	at, err := s.GetAgentType(typeID)
	if err != nil {
		return nil, err
	}
	if at != nil {
		for k, v := range at.Settings {
			if v != "" {
				out[k] = v
			}
		}
	}
	ov, err := s.GetAgentTypeOverride(agentID, typeID)
	if err != nil {
		return nil, err
	}
	if ov != nil {
		for k, v := range ov.Settings {
			if v != "" {
				out[k] = v
			}
		}
	}
	return out, nil
}

// ResolveTypePermissions merges the type baseline, per-capability type rules,
// and the user's per-(agent, type) override rules (last match wins).
func (s *Store) ResolveTypePermissions(agentID, typeID string) ([]nanoprotocol.PermissionRule, error) {
	rules := []nanoprotocol.PermissionRule{}
	at, err := s.GetAgentType(typeID)
	if err != nil {
		return nil, err
	}
	if at != nil {
		rules = append(rules, at.Permissions...)
		for _, c := range at.Capabilities {
			rules = append(rules, c.Permissions...)
		}
	}
	ov, err := s.GetAgentTypeOverride(agentID, typeID)
	if err != nil {
		return nil, err
	}
	if ov != nil {
		rules = append(rules, ov.Permissions...)
	}
	return rules, nil
}

// DefaultModeModel returns the effective catalog model id for an agent's default
// mode (its override model, else the type default) and the mode id. Used to
// seed the agent-level model/provider for the default session.
func (s *Store) DefaultModeModel(agentID string) (int64, string) {
	typeID := s.DefaultSessionAgent(agentID)
	if typeID == "" {
		// Nothing resolved the type from the session lookup: read the agent.
		if agent, err := s.GetAgent(agentID); err == nil && agent != nil {
			typeID = agent.TypeID
		}
	}
	if typeID == "" {
		typeID = DefaultAgentTypeID
	}
	if ov, err := s.GetAgentTypeOverride(agentID, typeID); err == nil && ov != nil && ov.ModelID != 0 {
		return s.EffectiveModelIDForType(typeID, ov.ModelID), typeID
	}
	return s.DefaultModelForType(typeID), typeID
}

// EffectiveModelIDForType resolves a selected model against a type's allowed
// set: a locked default wins; otherwise the selection if allowed; else default.
func (s *Store) EffectiveModelIDForType(typeID string, selected int64) int64 {
	at, err := s.GetAgentType(typeID)
	if err != nil || at == nil {
		return selected
	}
	var def int64
	var defLocked bool
	inSet := false
	for _, m := range at.Models {
		if m.ModelID == selected {
			inSet = true
		}
		if m.IsDefault {
			def = m.ModelID
			defLocked = m.Locked
		}
	}
	if defLocked {
		return def
	}
	if selected != 0 && inSet {
		return selected
	}
	return def
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
