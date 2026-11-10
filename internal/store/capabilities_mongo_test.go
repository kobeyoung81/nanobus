package store_test

// The capability catalog is five collections that share one shape of rule —
// platform rows for everyone, a user's own for the user — plus two per-user side
// tables. These tests assert behaviour rather than the SQL text they replaced; what has
// to hold is the visibility rule, the state-only writes (which must not touch the
// content an admin cannot edit) and the uniqueness the catalog depends on.

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/agenttypes"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func mcpFixture(id, scope, owner, name string) store.McpServerRecord {
	return store.McpServerRecord{
		Capability: store.Capability{
			ID: id, Scope: scope, OwnerUserID: owner, Name: name,
			Description: "d", DefaultEnabled: true,
		},
		Transport: "http",
		URL:       "http://localhost:1",
		Headers:   map[string]string{"X-Api-Key": "k"},
	}
}

func skillFixture(id, scope, owner, name string) store.SkillRecord {
	return store.SkillRecord{
		Capability: store.Capability{
			ID: id, Scope: scope, OwnerUserID: owner, Name: name,
			Description: "d", DefaultEnabled: true,
		},
		Files:  map[string]string{"SKILL.md": "body"},
		Origin: "human",
	}
}

// ======== builtin tools ========

// ======== mcp servers ========

func TestCreateMcp_ScopesAndLists(t *testing.T) {
	s := storetest.Connect(t)

	for _, rec := range []store.McpServerRecord{
		mcpFixture("mcp:platform", "platform", "", "Shared"),
		mcpFixture("mcp:mine", "user", "u-1", "Mine"),
		mcpFixture("mcp:theirs", "user", "u-2", "Theirs"),
	} {
		if err := s.CreateMcp(rec); err != nil {
			t.Fatalf("CreateMcp(%s): %v", rec.ID, err)
		}
	}

	servers, err := s.ListMcpServers("u-1")
	if err != nil {
		t.Fatalf("ListMcpServers: %v", err)
	}
	// Platform + own, ordered by name, and another user's is absent.
	if len(servers) != 2 || servers[0].ID != "mcp:mine" || servers[1].ID != "mcp:platform" {
		t.Fatalf("unexpected list: %+v", servers)
	}
	if servers[0].Headers["X-Api-Key"] != "k" {
		t.Fatalf("headers lost: %+v", servers[0])
	}

	rec, err := s.GetMcpServer("mcp:platform")
	if err != nil {
		t.Fatalf("GetMcpServer: %v", err)
	}
	if rec == nil || rec.Name != "Shared" || rec.Scope != "platform" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if _, err := s.GetMcpServer("mcp:ghost"); err == nil {
		t.Fatal("expected an error for a missing server")
	}
}

// TestCreateMcp_RefusesADuplicateNameForTheSameOwner pins ensureMcpNameFree,
// which is what stands in for a unique key this catalog never had.
func TestCreateMcp_RefusesADuplicateNameForTheSameOwner(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.CreateMcp(mcpFixture("mcp:mine", "user", "u-1", "Same")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.CreateMcp(mcpFixture("mcp:other", "user", "u-1", "Same")); err == nil {
		t.Fatal("a duplicate name for the same user was accepted")
	}
	// Another user may use the name.
	if err := s.CreateMcp(mcpFixture("mcp:theirs", "user", "u-2", "Same")); err != nil {
		t.Fatalf("another user's server was refused: %v", err)
	}
}

func TestUpdateMcp_ReplacesAndUpdatesState(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.CreateMcp(mcpFixture("mcp:mine", "user", "u-1", "Old")); err != nil {
		t.Fatalf("CreateMcp: %v", err)
	}
	updated := mcpFixture("mcp:mine", "user", "u-1", "New")
	updated.URL = "http://elsewhere:2"
	updated.Headers = map[string]string{"X-Other": "v"}
	if err := s.UpdateMcp("mcp:mine", updated); err != nil {
		t.Fatalf("UpdateMcp: %v", err)
	}

	rec, err := s.GetMcpServer("mcp:mine")
	if err != nil {
		t.Fatalf("GetMcpServer: %v", err)
	}
	if rec.Name != "New" || rec.URL != "http://elsewhere:2" || rec.Headers["X-Other"] != "v" {
		t.Fatalf("the update did not land: %+v", rec)
	}

	off := false
	if err := s.UpdateMcpState("mcp:mine", &off, nil); err != nil {
		t.Fatalf("UpdateMcpState: %v", err)
	}
	rec, err = s.GetMcpServer("mcp:mine")
	if err != nil {
		t.Fatalf("GetMcpServer: %v", err)
	}
	if rec.DefaultEnabled {
		t.Fatalf("state not written: %+v", rec)
	}
	if rec.Name != "New" {
		t.Fatalf("a state write touched the content: %+v", rec)
	}
	if err := s.UpdateMcpState("mcp:ghost", &off, nil); err == nil {
		t.Fatal("expected an error for a missing server")
	}
}

// TestDeleteMcp_ScopeDecidesTheFilter: a user-scoped delete only removes the
// caller's own row; the admin path (scope != "user") removes whatever the id
// names.
func TestDeleteMcp_ScopeDecidesTheFilter(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.CreateMcp(mcpFixture("mcp:mine", "user", "u-1", "Mine")); err != nil {
		t.Fatalf("CreateMcp: %v", err)
	}
	if err := s.DeleteMcp("mcp:mine", "u-2", "user"); err == nil {
		t.Fatal("another user deleted someone else's server")
	}
	if err := s.DeleteMcp("mcp:mine", "u-1", "user"); err != nil {
		t.Fatalf("the owner could not delete: %v", err)
	}
	if err := s.DeleteMcp("mcp:mine", "", "platform"); err == nil {
		t.Fatal("expected an error for a missing server")
	}
}

// ======== skills ========

func TestCreateSkill_AppliesTheDefaults(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.CreateSkill(store.SkillRecord{
		Capability: store.Capability{ID: "skill:a", Scope: "platform", Name: "a", DefaultEnabled: true},
		Files:      map[string]string{"SKILL.md": "body"},
	}); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}

	rec, err := s.GetSkill("skill:a")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if rec.Origin != "human" || rec.Status != "published" || rec.Version != 1 || rec.PublishedVersion != 1 {
		t.Fatalf("defaults not applied: %+v", rec)
	}
	if rec.Files["SKILL.md"] != "body" {
		t.Fatalf("files lost: %+v", rec.Files)
	}
}

// TestCreateSkill_NameIsGloballyUnique pins migration 050's unique key: a name is
// taken once, by anyone.
func TestCreateSkill_NameIsGloballyUnique(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.CreateSkill(skillFixture("skill:a", "platform", "", "shared-name")); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.CreateSkill(skillFixture("skill:b", "user", "u-1", "shared-name")); err == nil {
		t.Fatal("a duplicate skill name was accepted: the unique index is missing")
	}
}

// TestSkillNameTaken_PlatformNamesAreReserved: the seeder asks this before it
// inserts, and it must treat a platform name as taken for everybody.
func TestSkillNameTaken_PlatformNamesAreReserved(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.CreateSkill(skillFixture("skill:platform", "platform", "", "busy")); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	if taken, err := s.SkillNameTaken("busy", "u-1", ""); err != nil || !taken {
		t.Fatalf("a platform name should be taken for u-1: %v (err %v)", taken, err)
	}

	// Another user's skill does not reserve the name for this one.
	if err := s.CreateSkill(skillFixture("skill:theirs", "user", "u-2", "theirs")); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	if taken, err := s.SkillNameTaken("theirs", "u-1", ""); err != nil || taken {
		t.Fatalf("another user's name should be free for u-1: %v (err %v)", taken, err)
	}
	if taken, err := s.SkillNameTaken("theirs", "u-2", ""); err != nil || !taken {
		t.Fatalf("the owner's own name should be taken: %v (err %v)", taken, err)
	}
	// Excluding the skill itself makes its name free again.
	if taken, err := s.SkillNameTaken("theirs", "u-2", "skill:theirs"); err != nil || taken {
		t.Fatalf("excluding the skill should free its name: %v (err %v)", taken, err)
	}
}

func TestListSkills_ScopeAndOrder(t *testing.T) {
	s := storetest.Connect(t)

	for _, rec := range []store.SkillRecord{
		skillFixture("skill:z", "platform", "", "zeta"),
		skillFixture("skill:a", "platform", "", "alpha"),
		skillFixture("skill:m", "user", "u-1", "mine"),
		skillFixture("skill:t", "user", "u-2", "theirs"),
	} {
		if err := s.CreateSkill(rec); err != nil {
			t.Fatalf("CreateSkill(%s): %v", rec.ID, err)
		}
	}

	skills, err := s.ListSkills("u-1")
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	names := make([]string, 0, len(skills))
	for _, sk := range skills {
		names = append(names, sk.Name)
	}
	want := []string{"alpha", "mine", "zeta"}
	if len(names) != len(want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, names)
		}
	}
}

func TestGetUserSkillByName(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.CreateSkill(skillFixture("skill:mine", "user", "u-1", "mine")); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	rec, err := s.GetUserSkillByName("u-1", "mine")
	if err != nil {
		t.Fatalf("GetUserSkillByName: %v", err)
	}
	if rec.ID != "skill:mine" {
		t.Fatalf("unexpected skill: %+v", rec)
	}
	// Another user's skill, and a platform one, are not this user's.
	if _, err := s.GetUserSkillByName("u-2", "mine"); err == nil {
		t.Fatal("expected an error for another user's skill")
	}
}

func TestUpdateSkill_AndStateAreSeparate(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.CreateSkill(skillFixture("skill:mine", "user", "u-1", "mine")); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	updated := skillFixture("skill:mine", "user", "u-1", "mine2")
	updated.Description = "new"
	updated.Files = map[string]string{"SKILL.md": "new body"}
	updated.Version = 3
	updated.PublishedVersion = 3
	updated.ContentHash = "hash"
	updated.CreatedBy = "u-1"
	if err := s.UpdateSkill("skill:mine", updated); err != nil {
		t.Fatalf("UpdateSkill: %v", err)
	}

	rec, err := s.GetSkill("skill:mine")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if rec.Name != "mine2" || rec.Files["SKILL.md"] != "new body" || rec.Version != 3 || rec.CreatedBy != "u-1" {
		t.Fatalf("the update did not land: %+v", rec)
	}

	// Clearing created_by unsets it rather than storing an empty string
	// (NULLIF(?, '')).
	cleared := updated
	cleared.CreatedBy = ""
	if err := s.UpdateSkill("skill:mine", cleared); err != nil {
		t.Fatalf("UpdateSkill: %v", err)
	}
	if rec, err = s.GetSkill("skill:mine"); err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if rec.CreatedBy != "" {
		t.Fatalf("created_by was not cleared: %+v", rec)
	}

	// The state write leaves the content alone — including for a bundled skill,
	// whose content is not editable at all.
	on := true
	if err := s.UpdateSkillState("skill:mine", &on, nil); err != nil {
		t.Fatalf("UpdateSkillState: %v", err)
	}
	rec, err = s.GetSkill("skill:mine")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if !rec.DefaultEnabled || rec.Name != "mine2" {
		t.Fatalf("state write disturbed the content: %+v", rec)
	}
	if err := s.UpdateSkillState("skill:ghost", &on, nil); err == nil {
		t.Fatal("expected an error for a missing skill")
	}
}

func TestDeleteSkill_ScopeDecidesTheFilter(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.CreateSkill(skillFixture("skill:mine", "user", "u-1", "mine")); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}
	if err := s.DeleteSkill("skill:mine", "u-2", "user"); err == nil {
		t.Fatal("another user deleted someone else's skill")
	}
	if err := s.DeleteSkill("skill:mine", "u-1", "user"); err != nil {
		t.Fatalf("the owner could not delete: %v", err)
	}
	if err := s.DeleteSkill("skill:mine", "", "platform"); err == nil {
		t.Fatal("expected an error for a missing skill")
	}
}

// ======== per-user side tables ========

func TestUserSecrets_RoundTrip(t *testing.T) {
	s := storetest.Connect(t)

	for _, kv := range []struct{ key, value string }{
		{"API_KEY", "sk-1"}, {"TOKEN", "t-1"},
	} {
		if err := s.UpsertUserSecret("u-1", "skill", "skill:a", kv.key, kv.value); err != nil {
			t.Fatalf("UpsertUserSecret: %v", err)
		}
	}
	// Replacing a value keeps one row.
	if err := s.UpsertUserSecret("u-1", "skill", "skill:a", "API_KEY", "sk-2"); err != nil {
		t.Fatalf("UpsertUserSecret: %v", err)
	}

	secrets, err := s.ListUserSecrets("u-1", "skill", "skill:a")
	if err != nil {
		t.Fatalf("ListUserSecrets: %v", err)
	}
	if len(secrets) != 2 || secrets["API_KEY"] != "sk-2" || secrets["TOKEN"] != "t-1" {
		t.Fatalf("unexpected secrets: %+v", secrets)
	}
	// Scoped by user, kind and ref.
	if other, err := s.ListUserSecrets("u-2", "skill", "skill:a"); err != nil || len(other) != 0 {
		t.Fatalf("another user's secrets leaked: %+v (err %v)", other, err)
	}
	if other, err := s.ListUserSecrets("u-1", "mcp", "skill:a"); err != nil || len(other) != 0 {
		t.Fatalf("another kind's secrets leaked: %+v (err %v)", other, err)
	}

	if err := s.DeleteUserSecret("u-1", "skill", "skill:a", "API_KEY"); err != nil {
		t.Fatalf("DeleteUserSecret: %v", err)
	}
	if secrets, err = s.ListUserSecrets("u-1", "skill", "skill:a"); err != nil || len(secrets) != 1 {
		t.Fatalf("unexpected secrets after delete: %+v (err %v)", secrets, err)
	}
}

// TestAckSkill_KeepsTheHighestVersion pins GREATEST(): an acknowledgement that
// arrives out of order must not lower the recorded version.
func TestAckSkill_KeepsTheHighestVersion(t *testing.T) {
	s := storetest.Connect(t)

	if version, err := s.SkillAckVersion("u-1", "skill:a"); err != nil || version != 0 {
		t.Fatalf("an unacknowledged skill should read 0: %d (err %v)", version, err)
	}

	if err := s.AckSkill("u-1", "skill:a", 3); err != nil {
		t.Fatalf("AckSkill: %v", err)
	}
	if err := s.AckSkill("u-1", "skill:a", 1); err != nil {
		t.Fatalf("AckSkill: %v", err)
	}
	if version, err := s.SkillAckVersion("u-1", "skill:a"); err != nil || version != 3 {
		t.Fatalf("version = %d, want 3 (err %v)", version, err)
	}
	// A later acknowledgement moves it forward.
	if err := s.AckSkill("u-1", "skill:a", 5); err != nil {
		t.Fatalf("AckSkill: %v", err)
	}
	if version, err := s.SkillAckVersion("u-1", "skill:a"); err != nil || version != 5 {
		t.Fatalf("version = %d, want 5 (err %v)", version, err)
	}
	// Another user's acknowledgement is separate.
	if version, err := s.SkillAckVersion("u-2", "skill:a"); err != nil || version != 0 {
		t.Fatalf("another user's ack leaked: %d (err %v)", version, err)
	}
}

func TestMigrations_DeclareCapabilityIndexes(t *testing.T) {
	s := storetest.Connect(t)

	// A skill name is unique globally (migration 050).
	assertIndexKeys(t, s.DB().Collection("admin_skills"), "name_unique", [][2]any{{"name", int32(1)}})
	assertIndexUnique(t, s.DB().Collection("admin_skills"), "name_unique")
	// The list queries filter (scope, owner) and order by name.
	assertIndexKeys(t, s.DB().Collection("admin_skills"), "scope_owner_name", [][2]any{
		{"scope", int32(1)}, {"owner_user_id", int32(1)}, {"name", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("admin_mcp_servers"), "scope_owner_name", [][2]any{
		{"scope", int32(1)}, {"owner_user_id", int32(1)}, {"name", int32(1)},
	})
	// The two per-user tables keep their primary keys as unique indexes.
	assertIndexKeys(t, s.DB().Collection("auth_secrets"), "identity_unique", [][2]any{
		{"user_id", int32(1)}, {"capability_kind", int32(1)}, {"ref", int32(1)}, {"secret_key", int32(1)},
	})
	assertIndexUnique(t, s.DB().Collection("auth_secrets"), "identity_unique")
	assertIndexKeys(t, s.DB().Collection("admin_user_skill_acks"), "user_skill_unique", [][2]any{
		{"user_id", int32(1)}, {"skill_id", int32(1)},
	})
	assertIndexUnique(t, s.DB().Collection("admin_user_skill_acks"), "user_skill_unique")
}

// TestUserSecret_IdentityIsUnique pins the four-field unique index a secret's
// identity rests on: two upserts of the same secret must end as one row.
func TestUserSecret_IdentityIsUnique(t *testing.T) {
	s := storetest.Connect(t)

	for i := 0; i < 3; i++ {
		if err := s.UpsertUserSecret("u-1", "mcp", "mcp:a", "TOKEN", "v"); err != nil {
			t.Fatalf("UpsertUserSecret: %v", err)
		}
	}
	n, err := s.DB().Collection("auth_secrets").CountDocuments(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("count secrets: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected one document, got %d", n)
	}
}

// TestCapabilityVisible pins the resolution's own visibility check, used when an
// attachment is applied.
func TestCapabilityVisible(t *testing.T) {
	s := storetest.Connect(t)

	seedBuiltinTool(t, s, "shell")
	if err := s.CreateMcp(mcpFixture("mcp:mine", "user", "u-1", "Mine")); err != nil {
		t.Fatalf("CreateMcp: %v", err)
	}
	if err := s.CreateSkill(skillFixture("skill:platform", "platform", "", "shared")); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}

	cases := []struct {
		kind, ref, user string
		want            bool
	}{
		{"builtin_tool", "shell", "u-1", true},
		{"builtin_tool", "ghost", "u-1", false},
		{"mcp", "mcp:mine", "u-1", true},
		{"mcp", "mcp:mine", "u-2", false},
		{"skill", "skill:platform", "u-2", true},
		{"skill", "skill:ghost", "u-1", false},
		{"unknown", "whatever", "u-1", false},
	}
	for _, c := range cases {
		got, err := s.CapabilityVisible(c.kind, c.ref, c.user)
		if err != nil {
			t.Fatalf("CapabilityVisible(%s, %s): %v", c.kind, c.ref, err)
		}
		if got != c.want {
			t.Fatalf("CapabilityVisible(%s, %s, %s) = %v, want %v", c.kind, c.ref, c.user, got, c.want)
		}
	}
}

// TestCreateMcp_CompletesTheRestrictedBelts is the same rule for 081: an MCP
// server is inherited by default, so the read-only roles have to refuse it out
// loud, or their profiles never register as read-only.
func TestCreateMcp_CompletesTheRestrictedBelts(t *testing.T) {
	s := storetest.Connect(t)
	if _, err := s.SeedAgentTypes(agenttypes.Definitions()); err != nil {
		t.Fatalf("SeedAgentTypes: %v", err)
	}
	if err := s.CreateMcp(mcpFixture("mcp:search", "platform", "", "Search")); err != nil {
		t.Fatalf("CreateMcp: %v", err)
	}

	for _, belt := range agenttypes.CompleteBelts() {
		def, err := s.GetAgentType(belt.TypeID)
		if err != nil {
			t.Fatalf("GetAgentType(%s): %v", belt.TypeID, err)
		}
		found := false
		for _, c := range def.Capabilities {
			if string(c.Kind) != "mcp" || c.Ref != "mcp:search" {
				continue
			}
			found = true
			if c.DefaultEnabled != belt.McpEnabled {
				t.Fatalf("%s: mcp:search = %v, want %v", belt.TypeID, c.DefaultEnabled, belt.McpEnabled)
			}
		}
		if !found {
			t.Fatalf("%s has no decision about the new MCP server: %+v", belt.TypeID, def.Capabilities)
		}
	}
}

// seedBuiltinTool writes a catalogue row directly: announcing definitions is the
// admin module's write now, and this test only needs the row to exist.
func seedBuiltinTool(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if _, err := s.DB().Collection("admin_builtin_tools").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id}, {Key: "description", Value: ""}, {Key: "default_enabled", Value: true},
	}); err != nil {
		t.Fatalf("seed builtin tool %s: %v", id, err)
	}
}
