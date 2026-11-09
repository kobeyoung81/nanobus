package store_test

// The agent type is where the move changes the shape of the data rather than
// only the engine: three tables (a type, its models, its capability baseline)
// became one document, because that is what made the transaction in
// SaveAgentType disappear. These tests pin the observable behaviour the three
// tables provided — the defaults, the read ordering, the bindings being replaced
// wholesale, the seeded flag protecting structure, and the name uniqueness — plus
// the one guarantee the foreign key used to give the agents table.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/permissionkeys"
	"gitee.com/kobeyoung81/nanobus/internal/promptbundle"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// agentTypeFixture is a type with just the fields a test cares about; the name
// defaults to the id so two fixtures never collide on the unique index.
func agentTypeFixture(id string, settings map[string]string) nanoprotocol.AgentTypeDef {
	return nanoprotocol.AgentTypeDef{ID: id, Name: id, Settings: settings}
}

// seedPermissionCatalog wires the permission key catalog the bus reads when it
// validates a policy. The catalog lives in the admin module now, so the store
// takes it through a provider rather than a collection.
func seedPermissionCatalog(t *testing.T, s *store.Store) {
	t.Helper()
	s.SetReadOnlyPermissionKeySource(func() (map[string]nanoprotocol.PermissionAction, error) {
		out := map[string]nanoprotocol.PermissionAction{}
		for _, k := range permissionkeys.Keys() {
			if k.ReadOnly {
				out[k.ID] = k.DefaultAction
			}
		}
		return out, nil
	})
}

// seedOnlyReadOnlyKey wires a catalog holding just the given key, for a test
// that asserts what the validation does with one specific read-only tool.
func seedOnlyReadOnlyKey(t *testing.T, s *store.Store, id string, action nanoprotocol.PermissionAction) {
	t.Helper()
	s.SetReadOnlyPermissionKeySource(func() (map[string]nanoprotocol.PermissionAction, error) {
		return map[string]nanoprotocol.PermissionAction{id: action}, nil
	})
}

func TestSaveAgentType_RoundTripsTheAggregate(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	def := nanoprotocol.AgentTypeDef{
		ID: "build", Name: "Build", Description: "the default", Prompt: "you build",
		MaxRounds: 42, Role: "general",
		ChannelFallbacks: map[string]string{"telegram": "build"},
		Settings:         map[string]string{"session_mode": "continuous"},
		Permissions:      []nanoprotocol.PermissionRule{{Pattern: "read", Action: nanoprotocol.PermissionAllow}},
		Strategy:         nanoprotocol.StrategyReact,
		Context:          nanoprotocol.ContextWindowed,
		Output:           nanoprotocol.OutputStream,
		SessionBinding:   nanoprotocol.SessionBindingShared,
		Models: []nanoprotocol.TypeModelBinding{
			{ModelID: 7, IsDefault: true, Locked: true},
			{ModelID: 9},
		},
		Capabilities: []nanoprotocol.TypeCapabilityBinding{
			{Kind: "builtin_tool", Ref: "shell", DefaultEnabled: true, Locked: "on", Params: `{"cwd":"/tmp"}`},
			{Kind: "skill", Ref: "docs", DefaultEnabled: false,
				Permissions: []nanoprotocol.PermissionRule{{Pattern: "write", Action: nanoprotocol.PermissionAsk}}},
		},
	}
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if got == nil {
		t.Fatal("the type was not stored")
	}
	if got.Name != "Build" || got.Description != "the default" || got.Prompt != "you build" ||
		got.MaxRounds != 42 || got.Role != "general" {
		t.Fatalf("scalar fields lost: %+v", got)
	}
	if got.Settings["session_mode"] != "continuous" || got.ChannelFallbacks["telegram"] != "build" {
		t.Fatalf("maps lost: %+v", got)
	}
	if len(got.Permissions) != 1 || got.Permissions[0].Pattern != "read" {
		t.Fatalf("permissions lost: %+v", got.Permissions)
	}
	if len(got.Models) != 2 || got.Models[0].ModelID != 7 || !got.Models[0].IsDefault || !got.Models[0].Locked {
		t.Fatalf("models lost: %+v", got.Models)
	}
	if len(got.Capabilities) != 2 {
		t.Fatalf("capabilities lost: %+v", got.Capabilities)
	}
	// The ordering the SQL produced at read time: kind asc, ref asc.
	if got.Capabilities[0].Kind != "builtin_tool" || got.Capabilities[1].Kind != "skill" {
		t.Fatalf("capabilities are not in (kind, ref) order: %+v", got.Capabilities)
	}
	if got.Capabilities[0].Params != `{"cwd":"/tmp"}` || got.Capabilities[0].Locked != "on" {
		t.Fatalf("capability fields lost: %+v", got.Capabilities[0])
	}
	if len(got.Capabilities[1].Permissions) != 1 || got.Capabilities[1].Permissions[0].Action != nanoprotocol.PermissionAsk {
		t.Fatalf("per-capability permissions lost: %+v", got.Capabilities[1])
	}
}

// TestSaveAgentType_OrdersBindingsOnRead pins the ORDER BYs the SQL did: models
// by (is_default desc, model_id asc), capabilities by (kind, ref). They are
// applied on read now, so insertion order must not leak into the result.
func TestSaveAgentType_OrdersBindingsOnRead(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	def := agentTypeFixture("build", nil)
	def.Models = []nanoprotocol.TypeModelBinding{
		{ModelID: 30},
		{ModelID: 10},
		{ModelID: 20, IsDefault: true},
		{ModelID: 5},
	}
	def.Capabilities = []nanoprotocol.TypeCapabilityBinding{
		{Kind: "model", Ref: "z"},
		{Kind: "builtin_tool", Ref: "ls"},
	}
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	want := []int64{20, 5, 10, 30}
	for i, id := range want {
		if got.Models[i].ModelID != id {
			t.Fatalf("models order = %+v, want %v", got.Models, want)
		}
	}
	if got.Capabilities[0].Kind != "builtin_tool" || got.Capabilities[1].Kind != "model" {
		t.Fatalf("capabilities order = %+v", got.Capabilities)
	}
}

func TestSaveAgentType_AppliesExecutionDefaults(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	if err := s.SaveAgentType(agentTypeFixture("build", nil)); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}
	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if got.Strategy != "react" || got.Context != "windowed" ||
		got.Output != "stream" || got.SessionBinding != "shared" {
		t.Fatalf("execution defaults not applied: %+v", got)
	}
}

// TestSaveAgentType_ReplacesBindings covers the DELETE+INSERT the SQL did around
// each binding table: a save is the whole set, not a merge.
func TestSaveAgentType_ReplacesBindings(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	def := agentTypeFixture("build", nil)
	def.Models = []nanoprotocol.TypeModelBinding{{ModelID: 1}, {ModelID: 2, IsDefault: true}}
	def.Capabilities = []nanoprotocol.TypeCapabilityBinding{{Kind: "skill", Ref: "a"}, {Kind: "skill", Ref: "b"}}
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("first save: %v", err)
	}

	def.Models = []nanoprotocol.TypeModelBinding{{ModelID: 2, IsDefault: true}}
	def.Capabilities = []nanoprotocol.TypeCapabilityBinding{{Kind: "skill", Ref: "b"}}
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].ModelID != 2 {
		t.Fatalf("models were merged rather than replaced: %+v", got.Models)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0].Ref != "b" {
		t.Fatalf("capabilities were merged rather than replaced: %+v", got.Capabilities)
	}
}

// TestSaveAgentType_KeepsSeededStructure is the protection the seeded flag buys:
// an admin may rewrite the prompt or the bindings, but not how a built-in type
// runs.
// TestSaveAgentType_RejectsBreakingAReadOnlyKey: the catalog these rules are
// validated against is seeded into the store by the test itself, but the
// rejection must still happen, and before anything is written.
func TestSaveAgentType_RejectsBreakingAReadOnlyKey(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	def := agentTypeFixture("build", nil)
	def.Permissions = []nanoprotocol.PermissionRule{{Pattern: "read", Action: nanoprotocol.PermissionDeny}}
	if err := s.SaveAgentType(def); err == nil {
		t.Fatal("a write that relaxes a read-only permission key was accepted")
	}
	if got, err := s.GetAgentType("build"); err != nil || got != nil {
		t.Fatalf("the rejected save wrote the type: %+v (err %v)", got, err)
	}
}

func TestSaveAgentType_KeepsSeededStructure(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)
	seedType(t, s, "build", "", "")

	// The insert above leaves seeded unset; stamp it the way the seeder does.
	if _, err := s.DB().Collection("admin_agent_types").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "build"}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "seeded", Value: true},
			{Key: "role", Value: "general"},
			{Key: "output", Value: nanoprotocol.OutputCaller},
			{Key: "strategy", Value: nanoprotocol.StrategyOneshot},
		}}},
	); err != nil {
		t.Fatalf("stamp seeded: %v", err)
	}

	def := agentTypeFixture("build", nil)
	def.Prompt = "an admin rewrote the prompt"
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if !got.Seeded {
		t.Fatal("the seeded flag was cleared by an admin save")
	}
	if got.Output != nanoprotocol.OutputCaller || got.Strategy != nanoprotocol.StrategyOneshot || got.Role != "general" {
		t.Fatalf("structural fields of a seeded type were rewritten: %+v", got)
	}
	if got.Prompt != "an admin rewrote the prompt" {
		t.Fatalf("the admin's prompt edit was rejected: %q", got.Prompt)
	}
}

// TestSaveAgentType_PreservesCreatedAt: the SQL upsert updated every column but
// created_at, so a save must not restamp it.
func TestSaveAgentType_PreservesCreatedAt(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	if err := s.SaveAgentType(agentTypeFixture("build", nil)); err != nil {
		t.Fatalf("first save: %v", err)
	}
	created, updated, _ := typeTimes(t, s, "build")

	if err := s.SaveAgentType(agentTypeFixture("build", nil)); err != nil {
		t.Fatalf("second save: %v", err)
	}
	createdAgain, updatedAgain, _ := typeTimes(t, s, "build")

	if !createdAgain.Equal(created) {
		t.Fatalf("created_at moved on save: %v → %v", created, createdAgain)
	}
	if updatedAgain.Before(updated) {
		t.Fatalf("updated_at went backwards: %v → %v", updated, updatedAgain)
	}
}

// typeTimes reads the two timestamps the protocol does not carry.
func typeTimes(t *testing.T, s *store.Store, id string) (created, updated time.Time, promptVersion int) {
	t.Helper()
	var doc struct {
		CreatedAt     time.Time `bson:"created_at"`
		UpdatedAt     time.Time `bson:"updated_at"`
		PromptVersion int       `bson:"prompt_version"`
	}
	if err := s.DB().Collection("admin_agent_types").
		FindOne(context.Background(), bson.D{{Key: "_id", Value: id}}).Decode(&doc); err != nil {
		t.Fatalf("read type times %s: %v", id, err)
	}
	return doc.CreatedAt, doc.UpdatedAt, doc.PromptVersion
}

// TestSaveAgentType_NameIsUnique pins the manifest's unique index: name was a
// UNIQUE KEY on the SQL table, and two types sharing one would make the admin
// catalogue ambiguous.
func TestSaveAgentType_NameIsUnique(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	first := agentTypeFixture("build", nil)
	first.Name = "Shared Name"
	if err := s.SaveAgentType(first); err != nil {
		t.Fatalf("first save: %v", err)
	}

	second := agentTypeFixture("plan", nil)
	second.Name = "Shared Name"
	if err := s.SaveAgentType(second); err == nil {
		t.Fatal("a duplicate type name was accepted: the unique index is missing")
	}
}

func TestGetAgentType_MissingIsNil(t *testing.T) {
	s := storetest.Connect(t)

	got, err := s.GetAgentType("ghost")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for a missing type, got %+v", got)
	}
}

func TestListAgentTypes_OrderedByName(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	for _, f := range []struct{ id, name string }{
		{"plan", "Plan"}, {"build", "Build"}, {"explore", "Explore"},
	} {
		def := agentTypeFixture(f.id, nil)
		def.Name = f.name
		if err := s.SaveAgentType(def); err != nil {
			t.Fatalf("save %s: %v", f.id, err)
		}
	}

	types, err := s.ListAgentTypes(false)
	if err != nil {
		t.Fatalf("ListAgentTypes: %v", err)
	}
	if len(types) != 3 {
		t.Fatalf("expected 3 types, got %d", len(types))
	}
	if types[0].Name != "Build" || types[1].Name != "Explore" || types[2].Name != "Plan" {
		t.Fatalf("expected [Build Explore Plan], got %+v", types)
	}
}

// TestListAgentTypes_SessionOnlyFilter: the user-facing catalogue is restricted
// to definitions that stream into a shared session; a subagent (output=caller)
// and a utility (strategy=oneshot) are workers, not choices.
func TestListAgentTypes_SessionOnlyFilter(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	// Defaults already make "build" a session type.
	if err := s.SaveAgentType(agentTypeFixture("build", nil)); err != nil {
		t.Fatalf("save build: %v", err)
	}
	subagent := agentTypeFixture("scout", nil)
	subagent.Output = nanoprotocol.OutputCaller
	if err := s.SaveAgentType(subagent); err != nil {
		t.Fatalf("save scout: %v", err)
	}
	// The filter is (output, session_binding) only — strategy is not part of it,
	// so a utility is excluded by binding it to a scratch session instead.
	utility := agentTypeFixture("title", nil)
	utility.Strategy = nanoprotocol.StrategyOneshot
	utility.SessionBinding = "scratch"
	if err := s.SaveAgentType(utility); err != nil {
		t.Fatalf("save title: %v", err)
	}

	sessionTypes, err := s.ListAgentTypes(true)
	if err != nil {
		t.Fatalf("ListAgentTypes: %v", err)
	}
	if len(sessionTypes) != 1 || sessionTypes[0].ID != "build" {
		t.Fatalf("expected only [build], got %+v", sessionTypes)
	}

	all, err := s.ListAgentTypes(false)
	if err != nil {
		t.Fatalf("ListAgentTypes: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected all 3 types, got %d", len(all))
	}
}

func TestListAgentTypes_EmptyIsASlice(t *testing.T) {
	s := storetest.Connect(t)

	types, err := s.ListAgentTypes(false)
	if err != nil {
		t.Fatalf("ListAgentTypes: %v", err)
	}
	if types == nil {
		t.Fatal("an empty catalogue must marshal as [] rather than null")
	}
}

func TestDefaultModelForType(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	def := agentTypeFixture("build", nil)
	def.Models = []nanoprotocol.TypeModelBinding{{ModelID: 9}, {ModelID: 4, IsDefault: true}, {ModelID: 6, IsDefault: true}}
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}
	// The lowest id among the defaults, not the lowest id.
	if got := s.DefaultModelForType("build"); got != 4 {
		t.Fatalf("DefaultModelForType = %d, want 4", got)
	}

	noDefault := agentTypeFixture("plan", nil)
	noDefault.Models = []nanoprotocol.TypeModelBinding{{ModelID: 8}, {ModelID: 3}}
	if err := s.SaveAgentType(noDefault); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}
	if got := s.DefaultModelForType("plan"); got != 3 {
		t.Fatalf("DefaultModelForType = %d, want the lowest model 3", got)
	}

	bare := agentTypeFixture("explore", nil)
	if err := s.SaveAgentType(bare); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}
	if got := s.DefaultModelForType("explore"); got != 0 {
		t.Fatalf("DefaultModelForType = %d, want 0 for a type with no models", got)
	}
	if got := s.DefaultModelForType("ghost"); got != 0 {
		t.Fatalf("DefaultModelForType = %d, want 0 for a missing type", got)
	}
}

// TestSetTypeCapabilityState_UpdatesTheElementInPlace: the SQL upsert wrote the
// two state columns and left the rest of the row alone, so a capability's params
// and per-capability permissions must survive an admin toggling it.
func TestSetTypeCapabilityState_UpdatesTheElementInPlace(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	def := agentTypeFixture("build", nil)
	def.Capabilities = []nanoprotocol.TypeCapabilityBinding{
		{Kind: "skill", Ref: "docs", DefaultEnabled: true, Params: `{"x":1}`,
			Permissions: []nanoprotocol.PermissionRule{{Pattern: "write", Action: nanoprotocol.PermissionAsk}}},
	}
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}

	if err := s.SetTypeCapabilityState("build", "skill", "docs", false, "off"); err != nil {
		t.Fatalf("SetTypeCapabilityState: %v", err)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if len(got.Capabilities) != 1 {
		t.Fatalf("the capability was duplicated rather than updated: %+v", got.Capabilities)
	}
	c := got.Capabilities[0]
	if c.DefaultEnabled || c.Locked != "off" {
		t.Fatalf("state not written: %+v", c)
	}
	if c.Params != `{"x":1}` || len(c.Permissions) != 1 {
		t.Fatalf("the update dropped fields it does not own: %+v", c)
	}
}

// TestSetTypeCapabilityState_AppendsWhenAbsent: the SQL inserted a missing row.
func TestSetTypeCapabilityState_AppendsWhenAbsent(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	if err := s.SaveAgentType(agentTypeFixture("build", nil)); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}
	if err := s.SetTypeCapabilityState("build", "mcp", "github", true, "on"); err != nil {
		t.Fatalf("SetTypeCapabilityState: %v", err)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if len(got.Capabilities) != 1 {
		t.Fatalf("expected the capability to be appended: %+v", got.Capabilities)
	}
	if got.Capabilities[0].Kind != "mcp" || got.Capabilities[0].Ref != "github" ||
		!got.Capabilities[0].DefaultEnabled || got.Capabilities[0].Locked != "on" {
		t.Fatalf("appended capability is wrong: %+v", got.Capabilities[0])
	}

	// Repeating it must not append a second copy.
	if err := s.SetTypeCapabilityState("build", "mcp", "github", false, ""); err != nil {
		t.Fatalf("SetTypeCapabilityState: %v", err)
	}
	got, err = s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0].DefaultEnabled {
		t.Fatalf("a repeated write duplicated or failed to update: %+v", got.Capabilities)
	}
}

// TestSetTypeCapabilityState_SameRefOtherKind: the SQL's primary key was
// (type_id, kind, ref), so a ref is only unique within its kind. Matching on the
// ref alone would silently write the wrong capability.
func TestSetTypeCapabilityState_SameRefOtherKind(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	def := agentTypeFixture("build", nil)
	def.Capabilities = []nanoprotocol.TypeCapabilityBinding{
		{Kind: "skill", Ref: "docs", DefaultEnabled: true, Locked: "on"},
	}
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}

	if err := s.SetTypeCapabilityState("build", "mcp", "docs", false, ""); err != nil {
		t.Fatalf("SetTypeCapabilityState: %v", err)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if len(got.Capabilities) != 2 {
		t.Fatalf("expected two capabilities (same ref, different kind): %+v", got.Capabilities)
	}
	for _, c := range got.Capabilities {
		if c.Kind == "skill" && (c.Locked != "on" || !c.DefaultEnabled) {
			t.Fatalf("the skill capability was written instead of the mcp one: %+v", c)
		}
		if c.Kind == "mcp" && (c.Locked != "" || c.DefaultEnabled) {
			t.Fatalf("the mcp capability was not written: %+v", c)
		}
	}
}

// TestSetTypeCapabilityState_UnknownType: the SQL had no foreign key on that
// table, so a row could be inserted for a type that did not exist. An embedded
// capability needs its parent, and a nameless parent would appear in the admin
// catalogue — so this is an error.
func TestSetTypeCapabilityState_UnknownType(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.SetTypeCapabilityState("ghost", "skill", "docs", true, ""); err == nil {
		t.Fatal("expected an error for an unknown type")
	}
	n, err := s.DB().Collection("admin_agent_types").CountDocuments(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("count types: %v", err)
	}
	if n != 0 {
		t.Fatalf("a stub type was created: %d documents", n)
	}
}

func TestDeleteTypeCapabilityState(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	def := agentTypeFixture("build", nil)
	def.Capabilities = []nanoprotocol.TypeCapabilityBinding{
		{Kind: "skill", Ref: "docs"},
		{Kind: "mcp", Ref: "docs"},
	}
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}

	if err := s.DeleteTypeCapabilityState("build", "skill", "docs"); err != nil {
		t.Fatalf("DeleteTypeCapabilityState: %v", err)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0].Kind != "mcp" {
		t.Fatalf("expected only the mcp capability to remain: %+v", got.Capabilities)
	}
}

func TestDeleteAgentType_RefusesSeeded(t *testing.T) {
	s := storetest.Connect(t)
	seedType(t, s, "build", "", "")
	if _, err := s.DB().Collection("admin_agent_types").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "build"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "seeded", Value: true}}}},
	); err != nil {
		t.Fatalf("stamp seeded: %v", err)
	}

	if err := s.DeleteAgentType("build"); err == nil {
		t.Fatal("a seeded type was deleted")
	}
	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if got == nil {
		t.Fatal("the seeded type is gone")
	}
}

func TestDeleteAgentType_Missing(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.DeleteAgentType("ghost"); err == nil {
		t.Fatal("expected an error for a missing type")
	}
}

// TestDeleteAgentType_ClearsTheAgentsThatUsedIt replaces what the foreign key did
// (ON DELETE SET NULL): without it, an agent keeps a type_id that resolves to no
// definition at all.
func TestDeleteAgentType_ClearsTheAgentsThatUsedIt(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)
	if err := s.SaveAgentType(agentTypeFixture("build", nil)); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}
	storetest.SeedAgents(t, s, "u-1", "agent-1") // seeded with type_id "build"

	if err := s.DeleteAgentType("build"); err != nil {
		t.Fatalf("DeleteAgentType: %v", err)
	}

	agent, err := s.GetAgent("agent-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if agent == nil {
		t.Fatal("the agent disappeared with its type")
	}
	if agent.TypeID != "" {
		t.Fatalf("the agent still points at the deleted type: %q", agent.TypeID)
	}
	if got, err := s.GetAgentType("build"); err != nil || got != nil {
		t.Fatalf("the type survived the delete: %+v (err %v)", got, err)
	}
}

// TestMigrations_DeclareAgentTypeIndexes pins the manifest. The unique index is
// a constraint, not an optimisation: without it two types could claim one name.
func TestMigrations_DeclareAgentTypeIndexes(t *testing.T) {
	s := storetest.Connect(t)

	assertIndexKeys(t, s.DB().Collection("admin_agent_types"), "name_unique", [][2]any{{"name", int32(1)}})
	assertIndexKeys(t, s.DB().Collection("admin_agent_types"), "session_catalog", [][2]any{
		{"output", int32(1)}, {"session_binding", int32(1)}, {"name", int32(1)},
	})
}

// TestAgentType_KeepsTheBaselineOutOfSettings pins where the bundled-prompt
// marker lives. Inside `settings` it would be a number in a map the API exposes
// as strings — which is what made the SQL readers drop the whole map.
func TestAgentType_KeepsTheBaselineOutOfSettings(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)
	if err := s.SaveAgentType(agentTypeFixture("build", map[string]string{"session_mode": "continuous"})); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}
	if _, _, err := s.SeedPlatformPrompts([]promptbundle.BundledPrompt{
		promptFixture("build", "bundled", 2),
	}, false); err != nil {
		t.Fatalf("SeedPlatformPrompts: %v", err)
	}
	if _, _, version := typeTimes(t, s, "build"); version != 2 {
		t.Fatalf("the baseline was not recorded: prompt_version = %d", version)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if _, ok := got.Settings["_prompt_hash"]; ok {
		t.Fatalf("the baseline leaked into settings: %+v", got.Settings)
	}
	if got.Settings["session_mode"] != "continuous" {
		t.Fatalf("settings lost: %+v", got.Settings)
	}
}

// Without a catalog the bus must refuse the save rather than skip the check: a
// policy that unlocks a read-only tool would otherwise pass silently whenever
// the admin module is unreachable.
func TestSaveAgentTypeFailsClosedWithoutThePermissionCatalog(t *testing.T) {
	s := storetest.Connect(t)

	err := s.SaveAgentType(nanoprotocol.AgentTypeDef{
		ID: "no-catalog", Name: "No catalog",
		Permissions: []nanoprotocol.PermissionRule{{Action: nanoprotocol.PermissionAllow, Pattern: "*"}},
	})
	if err == nil {
		t.Fatal("a save without a permission catalog was accepted")
	}
}
