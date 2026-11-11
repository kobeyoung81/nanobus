package store

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// DefaultAgentTypeID is the platform conversational type seeded by migration
// 049 (as `general`) and renamed to `build` by migration 060; agents without
// an explicit type fall back to it.
const DefaultAgentTypeID = "build"

// ======== AgentType CRUD ========

// agentTypesCollection holds a type together with its allowed models and its
// capability baseline.
//
// The three SQL tables became one document because they are one aggregate: the
// only way to write them together in SQL was the transaction SaveAgentType
// opened, and a standalone MongoDB has none. As one document, every write is a
// single update, so a type can never be read with half of its capabilities.
const agentTypesCollection = "admin_agent_types"

// storedPermissionRule is a permission rule as stored.
//
// An explicit mapping again: PermissionRule is a protocol type carrying only
// json tags, and decoding straight into it would depend on the driver
// lowercasing Go field names rather than on what the documents hold.
type storedPermissionRule struct {
	Pattern string                        `bson:"pattern"`
	Action  nanoprotocol.PermissionAction `bson:"action"`
}

// storedTypeModel is one allowed model of a type.
type storedTypeModel struct {
	ModelID   int64 `bson:"model_id"`
	IsDefault bool  `bson:"is_default"`
	Locked    bool  `bson:"locked"`
}

// storedTypeCapability is one capability baseline of a type.
type storedTypeCapability struct {
	Kind           nanoprotocol.CapabilityKind `bson:"kind"`
	Ref            string                      `bson:"ref"`
	DefaultEnabled bool                        `bson:"default_enabled"`
	Locked         string                      `bson:"locked,omitempty"`
	Params         string                      `bson:"params,omitempty"`
	Permissions    []storedPermissionRule      `bson:"permissions,omitempty"`
}

type storedAgentType struct {
	ID               string                 `bson:"_id"`
	Role             string                 `bson:"role,omitempty"`
	Name             string                 `bson:"name"`
	Description      string                 `bson:"description,omitempty"`
	Prompt           string                 `bson:"prompt,omitempty"`
	MaxRounds        int                    `bson:"max_rounds"`
	ChannelFallbacks map[string]string      `bson:"channel_fallbacks,omitempty"`
	Permissions      []storedPermissionRule `bson:"permissions,omitempty"`
	Settings         map[string]string      `bson:"settings,omitempty"`
	Seeded           bool                   `bson:"seeded"`
	Strategy         string                 `bson:"strategy"`
	Context          string                 `bson:"context"`
	Output           string                 `bson:"output"`
	SessionBinding   string                 `bson:"session_binding"`
	Models           []storedTypeModel      `bson:"models,omitempty"`
	Capabilities     []storedTypeCapability `bson:"capabilities,omitempty"`
	// PromptHash/PromptVersion are the bundled-prompt baseline (see promptseed.go).
	// They are top-level rather than entries in Settings: Settings is a map of
	// string-encoded runtime knobs, and a numeric version inside it is what made
	// the SQL readers drop the whole map.
	PromptHash    string    `bson:"prompt_hash,omitempty"`
	PromptVersion int       `bson:"prompt_version,omitempty"`
	CreatedAt     time.Time `bson:"created_at"`
	UpdatedAt     time.Time `bson:"updated_at"`
}

// def converts the document into the definition callers see.
//
// The bindings are ordered here rather than stored ordered, because the SQL
// ordered them at read time (models by is_default desc, model_id asc;
// capabilities by kind asc, ref asc) over rows whose insert order meant nothing.
// Sorting on read reproduces that output exactly, and keeps SetTypeCapabilityState
// from having to insert in the middle of an array.
func (d storedAgentType) def() nanoprotocol.AgentTypeDef {
	models := make([]nanoprotocol.TypeModelBinding, 0, len(d.Models))
	for _, m := range d.Models {
		models = append(models, nanoprotocol.TypeModelBinding{
			ModelID: m.ModelID, IsDefault: m.IsDefault, Locked: m.Locked,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].IsDefault != models[j].IsDefault {
			return models[i].IsDefault
		}
		return models[i].ModelID < models[j].ModelID
	})

	caps := make([]nanoprotocol.TypeCapabilityBinding, 0, len(d.Capabilities))
	for _, c := range d.Capabilities {
		caps = append(caps, nanoprotocol.TypeCapabilityBinding{
			Kind: c.Kind, Ref: c.Ref, DefaultEnabled: c.DefaultEnabled,
			Locked: c.Locked, Params: c.Params, Permissions: c.permissions(),
		})
	}
	sort.Slice(caps, func(i, j int) bool {
		if caps[i].Kind != caps[j].Kind {
			return caps[i].Kind < caps[j].Kind
		}
		return caps[i].Ref < caps[j].Ref
	})

	return nanoprotocol.AgentTypeDef{
		ID: d.ID, Role: d.Role, Name: d.Name, Description: d.Description,
		Prompt: d.Prompt, MaxRounds: d.MaxRounds,
		ChannelFallbacks: d.ChannelFallbacks, Permissions: d.permissions(),
		Settings: d.Settings, Seeded: d.Seeded,
		Strategy: d.Strategy, Context: d.Context, Output: d.Output,
		SessionBinding: d.SessionBinding,
		Models:         models, Capabilities: caps,
	}
}

func (d storedTypeCapability) permissions() []nanoprotocol.PermissionRule {
	if len(d.Permissions) == 0 {
		return nil
	}
	rules := make([]nanoprotocol.PermissionRule, 0, len(d.Permissions))
	for _, r := range d.Permissions {
		rules = append(rules, nanoprotocol.PermissionRule{Pattern: r.Pattern, Action: r.Action})
	}
	return rules
}

func (d storedAgentType) permissions() []nanoprotocol.PermissionRule {
	if len(d.Permissions) == 0 {
		return nil
	}
	rules := make([]nanoprotocol.PermissionRule, 0, len(d.Permissions))
	for _, r := range d.Permissions {
		rules = append(rules, nanoprotocol.PermissionRule{Pattern: r.Pattern, Action: r.Action})
	}
	return rules
}

func storedPermissionRules(rules []nanoprotocol.PermissionRule) []storedPermissionRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]storedPermissionRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, storedPermissionRule{Pattern: r.Pattern, Action: r.Action})
	}
	return out
}

// ListAgentTypes returns all agent definitions. When sessionOnly is true it is
// restricted to session-capable definitions (output=stream, binding=shared),
// which is what the user-facing assignable catalog needs.
func (s *Store) ListAgentTypes(sessionOnly bool) ([]nanoprotocol.AgentTypeDef, error) {
	ctx, cancel := opCtx()
	defer cancel()

	filter := bson.D{}
	if sessionOnly {
		filter = bson.D{
			{Key: "output", Value: nanoprotocol.OutputStream},
			{Key: "session_binding", Value: nanoprotocol.SessionBindingShared},
		}
	}
	// The unique index on `name` serves this sort, and it being unique means the
	// order is total without a tie-break.
	cur, err := s.db.Collection(agentTypesCollection).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list agent types: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []nanoprotocol.AgentTypeDef{}
	for cur.Next(ctx) {
		var doc storedAgentType
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode agent type: %w", err)
		}
		out = append(out, doc.def())
	}
	return out, cur.Err()
}

// isHelperDefinition reports whether a definition is a non-session worker the
// runtime can invoke: a subagent (output=caller) or a utility (strategy=oneshot).
func isHelperDefinition(t nanoprotocol.AgentTypeDef) bool {
	return t.Output == nanoprotocol.OutputCaller || t.Strategy == nanoprotocol.StrategyOneshot
}

// ResolveAgentDefinitions returns the unified registry view for an agent: its
// session agents (from agent_session_agents, default first) plus every helper
// definition (subagent/utility). Each carries Strategy/Context/Output so the
// runtime can drive all of them from one resolver.
func (s *Store) ResolveAgentDefinitions(agentID string) ([]nanoprotocol.AgentTypeDef, error) {
	seen := map[string]bool{}
	out := []nanoprotocol.AgentTypeDef{}
	add := func(t *nanoprotocol.AgentTypeDef) error {
		if t == nil || t.ID == "" || seen[t.ID] {
			return nil
		}
		seen[t.ID] = true
		if err := s.applyOverride(agentID, t); err != nil {
			return err
		}
		// Session modes (build/plan) share one head prompt, so the request prefix
		// does not change when the mode does — the upstream caches it by content and
		// a mode-specific head invalidated the whole conversation on every switch.
		// The mode's own prompt becomes the per-turn mode note instead.
		if IsSessionType(*t) {
			head, err := s.EffectiveSessionHead(agentID)
			if err != nil {
				return err
			}
			if head != "" {
				if t.Settings == nil {
					t.Settings = map[string]string{}
				}
				t.Settings[SessionModeNoteSettingKey] = t.Prompt
				t.Prompt = head
			}
		}
		out = append(out, *t)
		return nil
	}

	sessions, err := s.SessionAgentsForAgent(agentID)
	if err != nil {
		return nil, err
	}
	for _, sa := range sessions {
		t, err := s.GetAgentType(sa.DefID)
		if err != nil {
			return nil, err
		}
		if err := add(t); err != nil {
			return nil, err
		}
	}
	// Ensure the agent's primary type is present even if unlinked.
	primary, err := s.AgentTypeForAgent(agentID)
	if err != nil {
		return nil, err
	}
	if err := add(primary); err != nil {
		return nil, err
	}

	all, err := s.ListAgentTypes(false)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if isHelperDefinition(all[i]) {
			if err := add(&all[i]); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// applyOverride merges the user's (agent, type) override into a definition's
// effective settings/permissions/capabilities and attaches the raw override.
// Precedence: platform baseline -> user override, with admin locks winning.
func (s *Store) applyOverride(agentID string, t *nanoprotocol.AgentTypeDef) error {
	ov, err := s.GetAgentTypeOverride(agentID, t.ID)
	if err != nil || ov == nil {
		return err
	}
	mergeOverride(t, ov)
	return nil
}

// mergeOverride folds a user override into a definition's effective
// settings/permissions/capabilities and attaches the raw override.
func mergeOverride(t *nanoprotocol.AgentTypeDef, ov *nanoprotocol.AgentTypeOverride) {
	if ov == nil {
		return
	}
	t.Override = ov

	// Effective settings: baseline overlaid by the override (override wins).
	if len(ov.Settings) > 0 {
		merged := map[string]string{}
		for k, v := range t.Settings {
			merged[k] = v
		}
		for k, v := range ov.Settings {
			if v == "" {
				delete(merged, k)
				continue
			}
			merged[k] = v
		}
		t.Settings = merged
	}

	// Effective type-level permissions: baseline rules ++ override rules.
	// Per-capability rules stay on the capabilities (the runtime appends them).
	if len(ov.Permissions) > 0 {
		t.Permissions = append(append([]nanoprotocol.PermissionRule{}, t.Permissions...), ov.Permissions...)
	}

	// Effective capabilities: override replaces default_enabled unless locked.
	if len(ov.Capabilities) > 0 {
		pending := map[string]nanoprotocol.CapabilityAttachment{}
		for _, c := range ov.Capabilities {
			pending[string(c.Kind)+"/"+c.Ref] = c
		}
		for i := range t.Capabilities {
			key := string(t.Capabilities[i].Kind) + "/" + t.Capabilities[i].Ref
			c, ok := pending[key]
			if !ok {
				continue
			}
			switch t.Capabilities[i].Locked {
			case "on":
				t.Capabilities[i].DefaultEnabled = true
			case "off":
				t.Capabilities[i].DefaultEnabled = false
			default:
				t.Capabilities[i].DefaultEnabled = c.Enabled
			}
			delete(pending, key)
		}
		for _, c := range pending {
			t.Capabilities = append(t.Capabilities, nanoprotocol.TypeCapabilityBinding{
				Kind:           c.Kind,
				Ref:            c.Ref,
				DefaultEnabled: c.Enabled,
			})
		}
	}
}

// DefaultSessionAgent returns the def id of the agent's default session agent
// (the is_default link, else the first by position).
func (s *Store) DefaultSessionAgent(agentID string) string {
	sessions, err := s.SessionAgentsForAgent(agentID)
	if err != nil || len(sessions) == 0 {
		return ""
	}
	for _, sa := range sessions {
		if sa.IsDefault {
			return sa.DefID
		}
	}
	return sessions[0].DefID
}

// setCapabilityState builds the pipeline that writes default_enabled/locked onto
// one capability of a type document, appending the element when it is not there
// yet.
func setCapabilityState(kind nanoprotocol.CapabilityKind, ref string, defaultEnabled bool, locked string) bson.A {
	const cur = "$$cur"

	isTarget := bson.D{{Key: "$and", Value: bson.A{
		bson.D{{Key: "$eq", Value: bson.A{"$$c.kind", kind}}},
		bson.D{{Key: "$eq", Value: bson.A{"$$c.ref", ref}}},
	}}}
	state := bson.D{
		{Key: "kind", Value: kind},
		{Key: "ref", Value: ref},
		{Key: "default_enabled", Value: defaultEnabled},
		{Key: "locked", Value: locked},
	}
	// The existing element keeps everything this write does not name — params and
	// per-capability permissions in particular.
	merged := bson.D{{Key: "$map", Value: bson.D{
		{Key: "input", Value: cur},
		{Key: "as", Value: "c"},
		{Key: "in", Value: bson.D{{Key: "$cond", Value: bson.A{
			isTarget,
			bson.D{{Key: "$mergeObjects", Value: bson.A{"$$c", state}}},
			"$$c",
		}}}},
	}}}
	// The pair, not the ref alone: the same ref under two kinds is two different
	// capabilities.
	present := bson.D{{Key: "$gt", Value: bson.A{
		bson.D{{Key: "$size", Value: bson.D{{Key: "$filter", Value: bson.D{
			{Key: "input", Value: cur},
			{Key: "as", Value: "c"},
			{Key: "cond", Value: isTarget},
		}}}}},
		0,
	}}}

	return bson.A{bson.D{{Key: "$set", Value: bson.D{{Key: "capabilities", Value: bson.D{
		{Key: "$let", Value: bson.D{
			{Key: "vars", Value: bson.D{{Key: "cur", Value: bson.D{{Key: "$ifNull",
				Value: bson.A{"$capabilities", bson.A{}}}}}}},
			{Key: "in", Value: bson.D{{Key: "$concatArrays", Value: bson.A{
				merged,
				bson.D{{Key: "$cond", Value: bson.A{present, bson.A{}, bson.A{state}}}},
			}}}},
		}},
	}}}}}}
}

// DefaultModelForType returns the type's default model id (the binding flagged
// is_default, else the lowest-id allowed model). 0 when the type has none.
func (s *Store) DefaultModelForType(typeID string) int64 {
	// The SQL ordered by (is_default DESC, model_id ASC) and took the first row:
	// the lowest default model, or the lowest model when none is default. Asked
	// of the one document rather than of a sort on an embedded array.
	t, err := s.GetAgentType(typeID)
	if err != nil || t == nil {
		return 0
	}
	var best, lowest int64
	var haveBest, haveAny bool
	for _, m := range t.Models {
		if !haveAny || m.ModelID < lowest {
			lowest, haveAny = m.ModelID, true
		}
		if m.IsDefault && (!haveBest || m.ModelID < best) {
			best, haveBest = m.ModelID, true
		}
	}
	if haveBest {
		return best
	}
	return lowest
}

// GetAgentType loads one type (with its models + capability baseline).
func (s *Store) GetAgentType(id string) (*nanoprotocol.AgentTypeDef, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedAgentType
	err := s.db.Collection(agentTypesCollection).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent type: %w", err)
	}
	t := doc.def()
	return &t, nil
}
func storedTypeModels(models []nanoprotocol.TypeModelBinding) []storedTypeModel {
	if len(models) == 0 {
		return nil
	}
	out := make([]storedTypeModel, 0, len(models))
	for _, m := range models {
		out = append(out, storedTypeModel{ModelID: m.ModelID, IsDefault: m.IsDefault, Locked: m.Locked})
	}
	return out
}

func storedTypeCapabilities(caps []nanoprotocol.TypeCapabilityBinding) []storedTypeCapability {
	if len(caps) == 0 {
		return nil
	}
	out := make([]storedTypeCapability, 0, len(caps))
	for _, c := range caps {
		out = append(out, storedTypeCapability{
			Kind: c.Kind, Ref: c.Ref, DefaultEnabled: c.DefaultEnabled,
			Locked: c.Locked, Params: c.Params, Permissions: storedPermissionRules(c.Permissions),
		})
	}
	return out
}

// ======== permission keys ========
// AgentTypeForAgent returns the type an agent belongs to (nil if none).
func (s *Store) AgentTypeForAgent(agentID string) (*nanoprotocol.AgentTypeDef, error) {
	agent, err := s.GetAgent(agentID)
	if err != nil {
		return nil, fmt.Errorf("agent type for agent: %w", err)
	}
	if agent == nil || agent.TypeID == "" {
		return nil, nil
	}
	return s.GetAgentType(agent.TypeID)
}

// ======== helpers ========

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
