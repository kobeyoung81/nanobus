package store_test

// The per-(agent, mode) override is the user plane of the type resolution. These
// tests exist because a mock cannot see what matters here; what
// matters is that the override replaces what was stored rather than merging into
// it (the SQL wrote every column), and that the read-only key rule still refuses
// a policy that relaxes a locked permission.
//
// The read-only catalog lives in the same store now (seeded from the binary), so
// these need nothing mocked — seedPermissionCatalog, defined next door, writes
// it.

import (
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func TestSetAgentTypeOverride_RoundTrips(t *testing.T) {
	s := storetest.Connect(t)

	modelID := int64(7)
	want := nanoprotocol.AgentTypeOverride{
		ModelID:  modelID,
		Thinking: "high",
		Settings: map[string]string{"session_mode": "continuous"},
		Capabilities: []nanoprotocol.CapabilityAttachment{
			{Kind: "skill", Ref: "docs", Enabled: true},
		},
	}
	if err := s.SetAgentTypeOverride("agent-1", "plan", want); err != nil {
		t.Fatalf("SetAgentTypeOverride: %v", err)
	}

	got, err := s.GetAgentTypeOverride("agent-1", "plan")
	if err != nil {
		t.Fatalf("GetAgentTypeOverride: %v", err)
	}
	if got == nil {
		t.Fatal("the override was not stored")
	}
	if got.ModelID != modelID || got.Thinking != "high" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if got.Settings["session_mode"] != "continuous" {
		t.Fatalf("settings lost: %+v", got.Settings)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0].Ref != "docs" || !got.Capabilities[0].Enabled {
		t.Fatalf("capabilities lost: %+v", got.Capabilities)
	}
}

func TestGetAgentTypeOverride_NoneIsNil(t *testing.T) {
	s := storetest.Connect(t)

	got, err := s.GetAgentTypeOverride("agent-1", "build")
	if err != nil {
		t.Fatalf("GetAgentTypeOverride: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

// TestSetAgentTypeOverride_ClearsWhatIsNotSent: the SQL's upsert took every value
// from the statement, so a later save with no settings removed the earlier ones.
// A merge would leave an override the user believed they had cleared.
func TestSetAgentTypeOverride_ClearsWhatIsNotSent(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.SetAgentTypeOverride("agent-1", "build", nanoprotocol.AgentTypeOverride{
		ModelID:  7,
		Thinking: "high",
		Settings: map[string]string{"session_mode": "continuous"},
		Capabilities: []nanoprotocol.CapabilityAttachment{
			{Kind: "skill", Ref: "docs", Enabled: true},
		},
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// A second save that carries none of that: everything goes.
	if err := s.SetAgentTypeOverride("agent-1", "build", nanoprotocol.AgentTypeOverride{}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	got, err := s.GetAgentTypeOverride("agent-1", "build")
	if err != nil {
		t.Fatalf("GetAgentTypeOverride: %v", err)
	}
	if got == nil {
		t.Fatal("the override disappeared entirely")
	}
	if got.ModelID != 0 || got.Thinking != "" || len(got.Settings) != 0 || len(got.Capabilities) != 0 {
		t.Fatalf("the second save merged instead of replacing: %+v", got)
	}
}

// TestSetAgentTypeOverride_IsScopedToThePair pins the SQL primary key: one
// agent's override for a mode is not another's, and one mode's is not another's.
func TestSetAgentTypeOverride_IsScopedToThePair(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.SetAgentTypeOverride("agent-1", "build", nanoprotocol.AgentTypeOverride{Thinking: "high"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.SetAgentTypeOverride("agent-2", "build", nanoprotocol.AgentTypeOverride{Thinking: "low"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	for agentID, want := range map[string]string{"agent-1": "high", "agent-2": "low"} {
		got, err := s.GetAgentTypeOverride(agentID, "build")
		if err != nil {
			t.Fatalf("GetAgentTypeOverride(%s): %v", agentID, err)
		}
		if got == nil || got.Thinking != want {
			t.Fatalf("%s: got %+v, want thinking %q", agentID, got, want)
		}
	}
	if got, err := s.GetAgentTypeOverride("agent-1", "plan"); err != nil || got != nil {
		t.Fatalf("another mode returned an override: %+v (err %v)", got, err)
	}
}

func TestListAgentTypeOverrides_KeyedByType(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.SetAgentTypeOverride("agent-1", "build", nanoprotocol.AgentTypeOverride{Thinking: "high"}); err != nil {
		t.Fatalf("save build: %v", err)
	}
	if err := s.SetAgentTypeOverride("agent-1", "plan", nanoprotocol.AgentTypeOverride{Thinking: "low"}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := s.SetAgentTypeOverride("agent-2", "build", nanoprotocol.AgentTypeOverride{Thinking: "medium"}); err != nil {
		t.Fatalf("save other agent: %v", err)
	}

	overrides, err := s.ListAgentTypeOverrides("agent-1")
	if err != nil {
		t.Fatalf("ListAgentTypeOverrides: %v", err)
	}
	if len(overrides) != 2 {
		t.Fatalf("expected 2 overrides, got %+v", overrides)
	}
	if overrides["build"].Thinking != "high" || overrides["plan"].Thinking != "low" {
		t.Fatalf("unexpected overrides: %+v", overrides)
	}
}

func TestDeleteAgentTypeOverride(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.SetAgentTypeOverride("agent-1", "build", nanoprotocol.AgentTypeOverride{Thinking: "high"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := s.DeleteAgentTypeOverride("agent-1", "build"); err != nil {
		t.Fatalf("DeleteAgentTypeOverride: %v", err)
	}

	if got, err := s.GetAgentTypeOverride("agent-1", "build"); err != nil || got != nil {
		t.Fatalf("the override survived: %+v (err %v)", got, err)
	}
	// Deleting it again is not an error, as with the SQL DELETE.
	if err := s.DeleteAgentTypeOverride("agent-1", "build"); err != nil {
		t.Fatalf("DeleteAgentTypeOverride on a missing override: %v", err)
	}
}

func TestSetAgentTypeOverride_RequiresBothIDs(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.SetAgentTypeOverride("", "build", nanoprotocol.AgentTypeOverride{}); err == nil {
		t.Fatal("an empty agent id was accepted")
	}
	if err := s.SetAgentTypeOverride("agent-1", "", nanoprotocol.AgentTypeOverride{}); err == nil {
		t.Fatal("an empty type id was accepted")
	}
}

// TestSetAgentTypeOverride_RejectsReadOnlyOverride: the catalog's read-only keys
// must keep their default action however the rules are stacked.
func TestSetAgentTypeOverride_RejectsReadOnlyOverride(t *testing.T) {
	s := storetest.Connect(t)
	seedOnlyReadOnlyKey(t, s, "read", nanoprotocol.PermissionAllow)

	if err := s.SetAgentTypeOverride("agent-1", "build", nanoprotocol.AgentTypeOverride{
		Permissions: []nanoprotocol.PermissionRule{{Pattern: "read", Action: nanoprotocol.PermissionDeny}},
	}); err == nil {
		t.Fatal("expected read-only override to be rejected")
	}

	// A wildcard deny is refused too, unless the read-only key is re-allowed
	// after it.
	if err := s.SetAgentTypeOverride("agent-1", "build", nanoprotocol.AgentTypeOverride{
		Permissions: []nanoprotocol.PermissionRule{{Pattern: "*", Action: nanoprotocol.PermissionDeny}},
	}); err == nil {
		t.Fatal("expected wildcard deny to be rejected while the read-only key is not re-allowed")
	}
}

func TestSetAgentTypeOverride_AcceptsAllowlistAfterWildcardDeny(t *testing.T) {
	// A catalog with the one key the policy has to re-allow: with the shipped
	// catalog every other read-only tool would have to be re-allowed too.
	s := storetest.Connect(t)
	seedOnlyReadOnlyKey(t, s, "read", nanoprotocol.PermissionAllow)

	err := s.SetAgentTypeOverride("agent-1", "build", nanoprotocol.AgentTypeOverride{
		Permissions: []nanoprotocol.PermissionRule{
			{Pattern: "*", Action: nanoprotocol.PermissionDeny},
			{Pattern: "read", Action: nanoprotocol.PermissionAllow},
		},
	})
	if err != nil {
		t.Fatalf("SetAgentTypeOverride: %v", err)
	}

	got, err := s.GetAgentTypeOverride("agent-1", "build")
	if err != nil {
		t.Fatalf("GetAgentTypeOverride: %v", err)
	}
	if len(got.Permissions) != 2 {
		t.Fatalf("permissions lost: %+v", got.Permissions)
	}
	if got.Permissions[0].Pattern != "*" || got.Permissions[1].Action != nanoprotocol.PermissionAllow {
		t.Fatalf("permissions came back in a different shape: %+v", got.Permissions)
	}
}

// TestMigrations_DeclareOverrideIndexes pins the manifest, including the unique
// pair the SQL primary key covered.
func TestMigrations_DeclareOverrideIndexes(t *testing.T) {
	s := storetest.Connect(t)

	assertIndexKeys(t, s.DB().Collection("admin_agent_type_overrides"), "agent_type_unique", [][2]any{
		{"agent_id", int32(1)}, {"type_id", int32(1)},
	})
	assertIndexUnique(t, s.DB().Collection("admin_agent_type_overrides"), "agent_type_unique")
	assertIndexKeys(t, s.DB().Collection("admin_agent_type_overrides"), "type_id", [][2]any{{"type_id", int32(1)}})
}

// TestEffectiveTypeSettings_OverrideWins is the resolution the store exposes on
// top of the override: the type baseline, then the user's layer.
func TestEffectiveTypeSettings_OverrideWins(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	def := agentTypeFixture("build", map[string]string{"session_mode": "continuous", "shell_timeout": "30s"})
	if err := s.SaveAgentType(def); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}
	if err := s.SetAgentTypeOverride("agent-1", "build", nanoprotocol.AgentTypeOverride{
		Settings: map[string]string{"shell_timeout": "60s"},
	}); err != nil {
		t.Fatalf("SetAgentTypeOverride: %v", err)
	}

	settings, err := s.EffectiveTypeSettings("agent-1", "build")
	if err != nil {
		t.Fatalf("EffectiveTypeSettings: %v", err)
	}
	if settings["session_mode"] != "continuous" {
		t.Fatalf("the baseline was lost: %+v", settings)
	}
	if settings["shell_timeout"] != "60s" {
		t.Fatalf("the override did not win: %+v", settings)
	}
}

// TestSetModeCapability_Upserts moved here from capabilities_test.go: it is a
// write of the per-(agent, mode) override, so its store has to be one with
// MongoDB behind it. It asserts what the SQL version could not: that toggling the
// same capability replaces its entry instead of appending a second one.
func TestSetModeCapability_Upserts(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.SetModeCapability("agent-1", "build", "mcp", "mcp:github", true); err != nil {
		t.Fatalf("SetModeCapability: %v", err)
	}
	got, err := s.GetAgentTypeOverride("agent-1", "build")
	if err != nil {
		t.Fatalf("GetAgentTypeOverride: %v", err)
	}
	if got == nil || len(got.Capabilities) != 1 {
		t.Fatalf("expected one capability override, got %+v", got)
	}
	if got.Capabilities[0].Kind != "mcp" || got.Capabilities[0].Ref != "mcp:github" || !got.Capabilities[0].Enabled {
		t.Fatalf("unexpected override: %+v", got.Capabilities[0])
	}

	// Toggling it off updates that entry rather than adding another.
	if err := s.SetModeCapability("agent-1", "build", "mcp", "mcp:github", false); err != nil {
		t.Fatalf("SetModeCapability: %v", err)
	}
	got, err = s.GetAgentTypeOverride("agent-1", "build")
	if err != nil {
		t.Fatalf("GetAgentTypeOverride: %v", err)
	}
	if len(got.Capabilities) != 1 || got.Capabilities[0].Enabled {
		t.Fatalf("the capability was appended instead of updated: %+v", got.Capabilities)
	}
}
