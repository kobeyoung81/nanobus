package store_test

// The built-in agent types used to arrive as SQL migrations. They come from the
// binary now, so what has to hold is that a fresh store gets them, that a deploy
// never undoes an admin's edits, and — the one that would be a real bug — that
// the catalog the binary ships is *legal*: every definition it seeds must survive
// the read-only permission check that guards a save.

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanobus/internal/agenttypes"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func TestSeedAgentTypes_InsertsTheShippedCatalog(t *testing.T) {
	s := storetest.Connect(t)

	defs := agenttypes.Definitions()
	inserted, err := s.SeedAgentTypes(defs)
	if err != nil {
		t.Fatalf("SeedAgentTypes: %v", err)
	}
	if inserted != len(defs) {
		t.Fatalf("inserted %d of %d definitions", inserted, len(defs))
	}

	// Both session modes are there, marked as seeded so an admin save keeps their
	// structural fields.
	for _, id := range []string{"build", "plan"} {
		def, err := s.GetAgentType(id)
		if err != nil {
			t.Fatalf("GetAgentType(%s): %v", id, err)
		}
		if def == nil {
			t.Fatalf("%s was not seeded", id)
		}
		if !def.Seeded {
			t.Fatalf("%s is not marked seeded", id)
		}
		if def.Output != nanoprotocol.OutputStream || def.SessionBinding != nanoprotocol.SessionBindingShared {
			t.Fatalf("%s is not a session type: %+v", id, def)
		}
	}

	// The restricted belts arrived with their baselines.
	plan, err := s.GetAgentType("plan")
	if err != nil {
		t.Fatalf("GetAgentType(plan): %v", err)
	}
	refs := map[string]bool{}
	for _, c := range plan.Capabilities {
		refs[string(c.Kind)+"/"+c.Ref] = c.DefaultEnabled
	}
	for _, want := range []string{"builtin_tool/readonly_bash", "builtin_tool/readonly_task"} {
		if enabled, ok := refs[want]; !ok || !enabled {
			t.Fatalf("plan is missing the baseline %s: %+v", want, plan.Capabilities)
		}
	}
	// Which is the whole point of the read-only belt: plan cannot call the shell.
	for _, c := range plan.Permissions {
		if c.Pattern == "bash" || c.Pattern == "write" || c.Pattern == "edit" {
			t.Fatalf("plan allows a write-capable tool: %+v", c)
		}
	}

	// And the prompts are *not* here: they belong to the prompt bundle, which
	// tracks a baseline hash so an admin edit survives.
	if plan.Prompt != "" {
		t.Fatalf("the type seeder wrote a prompt: %q", plan.Prompt)
	}
}

// planBaselines is the count the shipped plan definition declares. A re-seed must
// restore what is missing and never append a second copy of what is there.
var planBaselines = func() int {
	for _, d := range agenttypes.Definitions() {
		if d.ID == "plan" {
			return len(d.Capabilities)
		}
	}
	panic("plan is not in the shipped catalog")
}()

func TestSeedAgentTypes_IsIdempotentAndKeepsEverything(t *testing.T) {
	s := storetest.Connect(t)
	if _, err := s.SeedAgentTypes(agenttypes.Definitions()); err != nil {
		t.Fatalf("SeedAgentTypes: %v", err)
	}

	// The admin renames the description, sets a prompt and turns one of plan's
	// baselines off.
	if _, err := s.DB().Collection("admin_agent_types").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "plan"}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "description", Value: "our plan mode"},
			{Key: "prompt", Value: "an admin's prompt"},
			{Key: "capabilities.$[c].default_enabled", Value: false},
		}}},
		options.UpdateOne().SetArrayFilters([]any{
			bson.D{{Key: "c.ref", Value: "readonly_bash"}},
		}),
	); err != nil {
		t.Fatalf("edit plan: %v", err)
	}

	again, err := s.SeedAgentTypes(agenttypes.Definitions())
	if err != nil {
		t.Fatalf("second SeedAgentTypes: %v", err)
	}
	if again != 0 {
		t.Fatalf("the second run inserted %d definitions", again)
	}

	plan, err := s.GetAgentType("plan")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if plan.Description != "our plan mode" || plan.Prompt != "an admin's prompt" {
		t.Fatalf("the deploy undid the admin's edit: %+v", plan)
	}
	if len(plan.Capabilities) != planBaselines {
		t.Fatalf("the baselines were duplicated or dropped: %+v", plan.Capabilities)
	}
	for _, c := range plan.Capabilities {
		if c.Ref == "readonly_bash" && c.DefaultEnabled {
			t.Fatalf("the admin-disabled baseline was re-enabled: %+v", c)
		}
	}
}

// TestSeedAgentTypes_AddsOnlyMissingBaselines: a later release giving an existing
// type a new tool must reach it, without resetting what is already there.
func TestSeedAgentTypes_AddsOnlyMissingBaselines(t *testing.T) {
	s := storetest.Connect(t)
	if _, err := s.SeedAgentTypes(agenttypes.Definitions()); err != nil {
		t.Fatalf("SeedAgentTypes: %v", err)
	}

	// An admin removes one baseline (the "inherit the platform default again"
	// affordance), and disables the other.
	if _, err := s.DB().Collection("admin_agent_types").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "plan"}},
		bson.D{{Key: "$pull", Value: bson.D{{Key: "capabilities", Value: bson.D{
			{Key: "ref", Value: "readonly_task"},
		}}}}},
	); err != nil {
		t.Fatalf("pull baseline: %v", err)
	}
	if _, err := s.DB().Collection("admin_agent_types").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "plan"}, {Key: "capabilities.ref", Value: "readonly_bash"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "capabilities.$.locked", Value: "off"}}}},
	); err != nil {
		t.Fatalf("lock baseline: %v", err)
	}

	if _, err := s.SeedAgentTypes(agenttypes.Definitions()); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	plan, err := s.GetAgentType("plan")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	byRef := map[string]nanoprotocol.TypeCapabilityBinding{}
	for _, c := range plan.Capabilities {
		byRef[c.Ref] = c
	}
	if _, ok := byRef["readonly_task"]; !ok {
		t.Fatalf("the missing baseline was not restored: %+v", plan.Capabilities)
	}
	if got := byRef["readonly_bash"].Locked; got != "off" {
		t.Fatalf("the admin's lock was reset: %+v", byRef["readonly_bash"])
	}
}

// TestShippedBelts_MatchTheDeployedDecisions pins the capability decisions that
// live in the data rather than in code.
//
// Migration 080 (and 081 for MCP) repaired the live rows: a role that lists only
// the tools it wants used to inherit everything it did not list, because an
// absent row means "catalog default", and the catalog default is on. Those two
// migrations lived only in the SQL set that has since been deleted, so the
// equivalent has to come from the definitions — otherwise a fresh installation
// hands a read-only role
// the write path. The numbers below are the frozen source database's rows.
func TestShippedBelts_MatchTheDeployedDecisions(t *testing.T) {
	defs := agenttypes.Definitions()
	byID := map[string]nanoprotocol.AgentTypeDef{}
	for _, d := range defs {
		byID[d.ID] = d
	}
	binding := func(id, ref string) (nanoprotocol.TypeCapabilityBinding, bool) {
		for _, c := range byID[id].Capabilities {
			if c.Ref == ref {
				return c, true
			}
		}
		return nanoprotocol.TypeCapabilityBinding{}, false
	}

	// The batch entry point is enabled where work happens…
	for _, id := range []string{"build", "plan", "role_general", "role_explore", "role_scout"} {
		c, ok := binding(id, "parallel_execution")
		if !ok || !c.DefaultEnabled {
			t.Errorf("%s has no enabled parallel_execution row: %+v", id, byID[id].Capabilities)
		}
	}
	// …and the read-only roles say "no" to delegation out loud, because the
	// catalog default is "on".
	for _, id := range []string{"role_explore", "role_scout", "role_general"} {
		c, ok := binding(id, "readonly_task")
		if !ok {
			t.Errorf("%s does not state its readonly_task default: %+v", id, byID[id].Capabilities)
		} else if c.DefaultEnabled {
			t.Errorf("%s enables delegation from a restricted role", id)
		}
	}
	if c, ok := binding("plan", "readonly_task"); !ok || !c.DefaultEnabled {
		t.Errorf("plan must be able to delegate read-only: %+v", byID["plan"].Capabilities)
	}

	// plan leads with a deny-all, so the batch tool needs an allow after it.
	allowed := false
	for _, rule := range byID["plan"].Permissions {
		if rule.Pattern == "parallel_execution" && rule.Action == nanoprotocol.PermissionAllow {
			allowed = true
		}
	}
	if !allowed {
		t.Errorf("plan's deny-all would reject its own batch tool: %+v", byID["plan"].Permissions)
	}
}

// TestShippedAgentTypes_SurviveTheirOwnValidation is the failure that would take
// the platform down: a seeded policy that the store refuses to save. Every
// definition here is saved through the same path an admin uses.
func TestShippedAgentTypes_SurviveTheirOwnValidation(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	for _, def := range agenttypes.Definitions() {
		// SaveAgentType validates the definition's rules (and its capabilities'
		// rules) against the read-only key catalog.
		if err := s.SaveAgentType(def); err != nil {
			t.Fatalf("the shipped definition %q cannot be saved: %v", def.ID, err)
		}
	}

	// The catalog itself is the admin module's now; its own tests cover seeding
	// and listing. What matters here is that every shipped definition passes the
	// read-only validation against it.
}

// TestSeedAgentTypes_SkipsEmptyIDs keeps a malformed entry out of the catalog.
func TestSeedAgentTypes_SkipsEmptyIDs(t *testing.T) {
	s := storetest.Connect(t)

	inserted, err := s.SeedAgentTypes([]nanoprotocol.AgentTypeDef{{Name: "no id"}, {
		ID: "with-id", Name: "With ID", Capabilities: []nanoprotocol.TypeCapabilityBinding{
			{Kind: "builtin_tool", Ref: "read", DefaultEnabled: true},
		},
	}})
	if err != nil {
		t.Fatalf("SeedAgentTypes: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted %d definitions, want 1", inserted)
	}
	if _, err := s.GetAgentType(""); err != nil {
		t.Fatalf("GetAgentType(\"\"): %v", err)
	}
	def, err := s.GetAgentType("with-id")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if def == nil || len(def.Capabilities) != 1 {
		t.Fatalf("unexpected definition: %+v", def)
	}
}
