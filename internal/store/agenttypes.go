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

// DefaultAgentType returns the id new agents default to.
func (s *Store) DefaultAgentTypeID() string { return DefaultAgentTypeID }

// ======== AgentType CRUD ========

// agentTypesCollection holds a type together with its allowed models and its
// capability baseline.
//
// The three SQL tables became one document because they are one aggregate: the
// only way to write them together in SQL was the transaction SaveAgentType
// opened, and a standalone MongoDB has none. As one document, every write is a
// single update, so a type can never be read with half of its capabilities.
const agentTypesCollection = "agent_types"

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

// SetTypeCapabilityState upserts the platform baseline (default_enabled/locked)
// for one capability on a type.
func (s *Store) SetTypeCapabilityState(typeID, kind, ref string, defaultEnabled bool, locked string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// One pipeline update, with the array derived server-side from its own
	// current value: the property the SQL got from the (type_id, kind, ref)
	// primary key, which an embedded array cannot carry. A read-modify-write in
	// Go would let two concurrent admin edits overwrite each other's capability.
	res, err := s.db.Collection(agentTypesCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: typeID}},
		setCapabilityState(nanoprotocol.CapabilityKind(kind), ref, defaultEnabled, locked),
	)
	if err != nil {
		return fmt.Errorf("set type capability state: %w", err)
	}
	// The SQL inserted its row whether or not the type existed — that table had
	// no foreign key. Embedding needs the parent document, and inventing an empty
	// one here would surface in the admin catalogue as a nameless type, so it is
	// reported instead.
	if res.MatchedCount == 0 {
		return fmt.Errorf("agent type %q not found", typeID)
	}
	return nil
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

// DeleteTypeCapabilityState removes a type's capability baseline so the type
// inherits the definition-plane (global) default again.
func (s *Store) DeleteTypeCapabilityState(typeID, kind, ref string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if _, err := s.db.Collection(agentTypesCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: typeID}},
		bson.D{{Key: "$pull", Value: bson.D{{Key: "capabilities", Value: bson.D{
			{Key: "kind", Value: kind},
			{Key: "ref", Value: ref},
		}}}}},
	); err != nil {
		return fmt.Errorf("delete type capability state: %w", err)
	}
	return nil
}

// getTypeCapabilityState returns the baseline state for a capability on a type
// (defaults TRUE/"" when absent).
func (s *Store) getTypeCapabilityState(typeID, kind, ref string) (bool, string, error) {
	ctx, cancel := opCtx()
	defer cancel()

	// The one element is read by fetching the (short) array rather than with a
	// positional $ projection: a type has a handful of capabilities, and the
	// projection's `capabilities.$` shape is a lot of machinery for that.
	var doc struct {
		Capabilities []storedTypeCapability `bson:"capabilities"`
	}
	err := s.db.Collection(agentTypesCollection).FindOne(ctx,
		bson.D{{Key: "_id", Value: typeID}},
		options.FindOne().SetProjection(bson.D{{Key: "capabilities", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return true, "", nil
	}
	if err != nil {
		return true, "", fmt.Errorf("get type capability state: %w", err)
	}
	for _, c := range doc.Capabilities {
		if string(c.Kind) == kind && c.Ref == ref {
			return c.DefaultEnabled, c.Locked, nil
		}
	}
	return true, "", nil
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

// ApplyModelToBuiltInTypes gives one platform model to every seeded agent type:
// the console's "apply to built-in agent types" action, which is what makes a
// model usable by the agents that exist before anyone edits a type by hand.
//
// Fill-only on purpose. A type that already lists the model is skipped, and the
// model becomes a type's default only when that type has no default yet — an
// administrator's per-type defaults are never rewritten. Running it twice
// therefore changes nothing.
func (s *Store) ApplyModelToBuiltInTypes(modelID int64) (applied, already, defaults []string, err error) {
	model, err := s.GetModel(modelID)
	if err != nil {
		return nil, nil, nil, err
	}
	if model == nil {
		return nil, nil, nil, ErrModelNotFound
	}
	if model.Scope != "platform" {
		return nil, nil, nil, fmt.Errorf("model %d: %w", modelID, ErrNotPlatformModel)
	}

	types, err := s.ListAgentTypes(false)
	if err != nil {
		return nil, nil, nil, err
	}
	applied, already, defaults = []string{}, []string{}, []string{}
	for _, t := range types {
		if !t.Seeded {
			continue
		}
		bound, hasDefault := false, false
		for _, b := range t.Models {
			if b.ModelID == modelID {
				bound = true
			}
			if b.IsDefault {
				hasDefault = true
			}
		}
		if bound {
			already = append(already, t.ID)
			continue
		}
		binding := nanoprotocol.TypeModelBinding{ModelID: modelID, IsDefault: !hasDefault}
		t.Models = append(t.Models, binding)
		if err := s.SaveAgentType(t); err != nil {
			return applied, already, defaults, fmt.Errorf("apply model %d to %s: %w", modelID, t.ID, err)
		}
		applied = append(applied, t.ID)
		if binding.IsDefault {
			defaults = append(defaults, t.ID)
		}
	}
	return applied, already, defaults, nil
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

// SaveAgentType upserts a type and replaces its model + capability bindings.
// The seeded flag is preserved for platform-seeded types.
func (s *Store) SaveAgentType(t nanoprotocol.AgentTypeDef) error {
	if t.ID == "" {
		return fmt.Errorf("agent type id is required")
	}
	// Built-in (seeded) definitions keep their structural identity: an admin may
	// edit prompt/models/permissions/settings but not the execution attributes
	// or invocation role.
	if existing, err := s.GetAgentType(t.ID); err == nil && existing != nil && existing.Seeded {
		t.Role = existing.Role
		t.Strategy = existing.Strategy
		t.Context = existing.Context
		t.Output = existing.Output
		t.SessionBinding = existing.SessionBinding
	}

	// Read-only keys must keep their catalog default across the type baseline
	// and its capability rules, mirroring the ResolvePermissions merge order.
	// The catalog itself (permission_keys) is seeded from the binary rather than
	// by a migration: it is a flat list the code owns.
	combined := append([]nanoprotocol.PermissionRule{}, t.Permissions...)
	for _, c := range t.Capabilities {
		combined = append(combined, c.Permissions...)
	}
	if err := s.validateReadOnlyRules(combined); err != nil {
		return err
	}

	doc := storedAgentType{
		ID: t.ID, Role: t.Role, Name: t.Name, Description: t.Description,
		Prompt: t.Prompt, MaxRounds: t.MaxRounds,
		ChannelFallbacks: t.ChannelFallbacks, Permissions: storedPermissionRules(t.Permissions),
		Settings: t.Settings, Seeded: t.Seeded,
		Strategy:       defaultStr(t.Strategy, "react"),
		Context:        defaultStr(t.Context, "windowed"),
		Output:         defaultStr(t.Output, "stream"),
		SessionBinding: defaultStr(t.SessionBinding, "shared"),
		Models:         storedTypeModels(t.Models),
		Capabilities:   storedTypeCapabilities(t.Capabilities),
	}

	ctx, cancel := opCtx()
	defer cancel()

	// $set of the fields an admin owns, plus $setOnInsert of created_at, rather
	// than a replace of the document:
	//
	//   - `seeded` is deliberately absent, as it was from the SQL column list:
	//     it marks a platform-seeded type, so neither an admin save nor a create
	//     may move it (a new type is simply not seeded);
	//   - created_at was not in the SQL upsert either, and a replace would drop
	//     a value the backfill carries over.
	// The prompt baseline (prompt_hash/prompt_version) is likewise the seeder's
	// to write, not this method's.
	now := time.Now().UTC()
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "role", Value: doc.Role},
			{Key: "name", Value: doc.Name},
			{Key: "description", Value: doc.Description},
			{Key: "prompt", Value: doc.Prompt},
			{Key: "max_rounds", Value: doc.MaxRounds},
			{Key: "channel_fallbacks", Value: doc.ChannelFallbacks},
			{Key: "permissions", Value: doc.Permissions},
			{Key: "settings", Value: doc.Settings},
			{Key: "strategy", Value: doc.Strategy},
			{Key: "context", Value: doc.Context},
			{Key: "output", Value: doc.Output},
			{Key: "session_binding", Value: doc.SessionBinding},
			{Key: "models", Value: doc.Models},
			{Key: "capabilities", Value: doc.Capabilities},
			{Key: "updated_at", Value: now},
		}},
		{Key: "$setOnInsert", Value: bson.D{{Key: "created_at", Value: now}}},
	}
	err := retryOnDuplicateUpsert(func() error {
		_, err := s.db.Collection(agentTypesCollection).UpdateOne(ctx,
			bson.D{{Key: "_id", Value: t.ID}}, update, options.UpdateOne().SetUpsert(true))
		return err
	})
	if err != nil {
		return fmt.Errorf("upsert agent type: %w", err)
	}
	return nil
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

// DeleteAgentType removes an admin-created type; seeded types are protected.
func (s *Store) DeleteAgentType(id string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// The type is read before anything is written: a seeded type is refused, and
	// its agents must not have been touched by the time that is known.
	existing, err := s.GetAgentType(id)
	if err != nil {
		return err
	}
	if existing == nil || existing.Seeded {
		return fmt.Errorf("agent type not found or is seeded")
	}

	// The SQL let the foreign key clear the reference (ON DELETE SET NULL).
	// Nothing does that here, and an agent left pointing at a deleted type
	// resolves to no definition at all — so the agents are cleared first, and a
	// failure between the two steps leaves a type with no agents rather than
	// agents with no type.
	if _, err := s.db.Collection(agentsCollection).UpdateMany(ctx,
		bson.D{{Key: "type_id", Value: id}},
		bson.D{
			{Key: "$unset", Value: bson.D{{Key: "type_id", Value: ""}}},
			{Key: "$set", Value: bson.D{{Key: "updated_at", Value: time.Now().UTC()}}},
		},
	); err != nil {
		return fmt.Errorf("clear agents of type %q: %w", id, err)
	}

	res, err := s.db.Collection(agentTypesCollection).DeleteOne(ctx,
		bson.D{
			{Key: "_id", Value: id},
			// A missing seeded field means not seeded, so this is `seeded = 0`.
			{Key: "seeded", Value: bson.D{{Key: "$ne", Value: true}}},
		})
	if err != nil {
		return fmt.Errorf("delete agent type: %w", err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("agent type not found or is seeded")
	}
	return nil
}

// SeedAgentTypes inserts the built-in definitions this binary ships, with the
// capability baselines they start with.
//
// It only ever inserts: a type's prompt, permissions, settings, models and
// baselines are all editable (the portal edits the first three), and a seeder
// that overwrote them would undo an admin's work on every deploy. A definition
// that already exists is therefore left completely alone — including a *seeded*
// one, whose structure SaveAgentType already protects.
//
// Note what is not copied: the platform default each tool carries. That lives on
// the tool definition (builtin_tools.default_enabled / locked), and a baseline
// element that is absent means "inherit it".
func (s *Store) SeedAgentTypes(defs []nanoprotocol.AgentTypeDef) (inserted int, err error) {
	ctx, cancel := opCtx()
	defer cancel()
	coll := s.db.Collection(agentTypesCollection)

	for _, t := range defs {
		if t.ID == "" {
			continue
		}
		now := time.Now().UTC()
		update := bson.D{{Key: "$setOnInsert", Value: bson.D{
			{Key: "role", Value: t.Role},
			{Key: "name", Value: t.Name},
			{Key: "description", Value: t.Description},
			{Key: "max_rounds", Value: t.MaxRounds},
			{Key: "channel_fallbacks", Value: t.ChannelFallbacks},
			{Key: "permissions", Value: storedPermissionRules(t.Permissions)},
			{Key: "settings", Value: t.Settings},
			{Key: "strategy", Value: defaultStr(t.Strategy, "react")},
			{Key: "context", Value: defaultStr(t.Context, "windowed")},
			{Key: "output", Value: defaultStr(t.Output, "stream")},
			{Key: "session_binding", Value: defaultStr(t.SessionBinding, "shared")},
			// The flag is the point: it is what makes an admin's later save keep the
			// execution attributes this catalog owns.
			{Key: "seeded", Value: true},
			{Key: "created_at", Value: now},
			{Key: "updated_at", Value: now},
		}}}
		res, err := coll.UpdateOne(ctx, bson.D{{Key: "_id", Value: t.ID}}, update,
			options.UpdateOne().SetUpsert(true))
		if err != nil {
			return inserted, fmt.Errorf("seed agent type %q: %w", t.ID, err)
		}
		inserted += int(res.UpsertedCount)

		// Baselines are the one part that is added to an existing type: a
		// definition that is already there may still be missing the tools a later
		// release gave it. The filter makes it an "append only if absent", so an
		// admin's toggle (or a lock) is never reset.
		for _, c := range t.Capabilities {
			if _, err := coll.UpdateOne(ctx,
				bson.D{
					{Key: "_id", Value: t.ID},
					{Key: "capabilities", Value: bson.D{{Key: "$not", Value: bson.D{{Key: "$elemMatch",
						Value: bson.D{{Key: "kind", Value: c.Kind}, {Key: "ref", Value: c.Ref}}}}}}},
				},
				bson.D{{Key: "$push", Value: bson.D{{Key: "capabilities", Value: storedTypeCapability{
					Kind: c.Kind, Ref: c.Ref, DefaultEnabled: c.DefaultEnabled, Locked: c.Locked,
				}}}}},
			); err != nil {
				return inserted, fmt.Errorf("seed agent type %q baseline %s/%s: %w", t.ID, c.Kind, c.Ref, err)
			}
		}
	}
	return inserted, nil
}

// ======== permission keys ========

const permissionKeysCollection = "permission_keys"

// storedPermissionKey is a catalog entry.
//
// No migration manifest names this collection: every read is by _id — including
// the listing, which orders by it — and MongoDB indexes _id with the collection.
type storedPermissionKey struct {
	ID            string                        `bson:"_id"`
	Description   string                        `bson:"description,omitempty"`
	DefaultAction nanoprotocol.PermissionAction `bson:"default_action"`
	ReadOnly      bool                          `bson:"read_only"`
	UpdatedAt     time.Time                     `bson:"updated_at"`
}

// SeedPermissionKeys fills the catalog from the binary.
//
// It inserts what is missing and touches nothing that is there: the catalog is
// admin-editable (the portal edits a key's description and default action), and a
// seed that overwrote them would undo that on every deploy.
//
// Retiring a key is deliberately *not* this method's job. A seeder runs from
// whatever binary is deployed, and during a rollback that is an older one — which
// must not be able to delete a key a newer binary added. Retirements (the SQL set
// had two: `websearch` and the pre-046 `skill`) are one-off, migration-shaped
// events.
func (s *Store) SeedPermissionKeys(keys []nanoprotocol.PermissionKeyDef) (inserted int, err error) {
	ctx, cancel := opCtx()
	defer cancel()

	now := time.Now().UTC()
	for _, k := range keys {
		if k.ID == "" {
			continue
		}
		update := bson.D{{Key: "$setOnInsert", Value: bson.D{
			{Key: "description", Value: k.Description},
			{Key: "default_action", Value: k.DefaultAction},
			{Key: "read_only", Value: k.ReadOnly},
			{Key: "updated_at", Value: now},
		}}}
		res, err := s.db.Collection(permissionKeysCollection).UpdateOne(ctx,
			bson.D{{Key: "_id", Value: k.ID}}, update, options.UpdateOne().SetUpsert(true))
		if err != nil {
			return inserted, fmt.Errorf("seed permission key %q: %w", k.ID, err)
		}
		inserted += int(res.UpsertedCount)
	}
	return inserted, nil
}

func (s *Store) ListPermissionKeys() ([]nanoprotocol.PermissionKeyDef, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(permissionKeysCollection).Find(ctx, bson.D{},
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list permission keys: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []nanoprotocol.PermissionKeyDef{}
	for cur.Next(ctx) {
		var doc storedPermissionKey
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode permission key: %w", err)
		}
		out = append(out, nanoprotocol.PermissionKeyDef{
			ID: doc.ID, Description: doc.Description,
			DefaultAction: doc.DefaultAction, ReadOnly: doc.ReadOnly,
		})
	}
	return out, cur.Err()
}

// readOnlyPermissionKeys returns the catalog's read-only keys mapped to their
// required default action.
func (s *Store) readOnlyPermissionKeys() (map[string]nanoprotocol.PermissionAction, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(permissionKeysCollection).Find(ctx,
		bson.D{{Key: "read_only", Value: true}})
	if err != nil {
		return nil, fmt.Errorf("list read-only permission keys: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := map[string]nanoprotocol.PermissionAction{}
	for cur.Next(ctx) {
		var doc storedPermissionKey
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode read-only permission key: %w", err)
		}
		out[doc.ID] = doc.DefaultAction
	}
	return out, cur.Err()
}

// validateReadOnlyRules rejects a policy that would move a read-only permission
// key away from its catalog default. The check is on the effective (last-match
// wins) action, so a wildcard baseline such as "*": deny is also rejected
// unless the read-only keys are explicitly re-allowed after it.
func (s *Store) validateReadOnlyRules(rules []nanoprotocol.PermissionRule) error {
	keys, err := s.readOnlyPermissionKeys()
	if err != nil {
		return err
	}
	for id, required := range keys {
		if got := nanoprotocol.EffectivePermission(id, rules); got != required {
			return fmt.Errorf("permission key %q is read-only and must remain %q (got %q)", id, required, got)
		}
	}
	return nil
}

// ======== resolution ========

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

// GetModePermissions returns the raw user override rules for one (agent, mode)
// without the type baseline. Empty when none are stored.
func (s *Store) GetModePermissions(agentID, typeID string) ([]nanoprotocol.PermissionRule, error) {
	ov, err := s.GetAgentTypeOverride(agentID, typeID)
	if err != nil || ov == nil {
		return []nanoprotocol.PermissionRule{}, err
	}
	if ov.Permissions == nil {
		return []nanoprotocol.PermissionRule{}, nil
	}
	return ov.Permissions, nil
}

// AppendModePermission adds or replaces one permission rule on an (agent, mode).
func (s *Store) AppendModePermission(agentID, typeID string, rule nanoprotocol.PermissionRule) error {
	rules, err := s.GetModePermissions(agentID, typeID)
	if err != nil {
		return err
	}
	found := false
	for i := range rules {
		if rules[i].Pattern == rule.Pattern {
			rules[i].Action = rule.Action
			found = true
		}
	}
	if !found {
		rules = append(rules, rule)
	}
	return s.SetModePermissions(agentID, typeID, rules)
}

// SetModePermissions replaces the override permission rules for an (agent, mode).
// Read-only validation happens in SetAgentTypeOverride.
func (s *Store) SetModePermissions(agentID, typeID string, rules []nanoprotocol.PermissionRule) error {
	ov, err := s.GetAgentTypeOverride(agentID, typeID)
	if err != nil {
		return err
	}
	if ov == nil {
		ov = &nanoprotocol.AgentTypeOverride{}
	}
	ov.Permissions = rules
	return s.SetAgentTypeOverride(agentID, typeID, *ov)
}

// ======== helpers ========

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
