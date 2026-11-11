package store_test

// The capability catalog is five collections that share one shape of rule —
// platform rows for everyone, a user's own for the user — plus two per-user side
// tables. These tests assert behaviour rather than the SQL text they replaced; what has
// to hold is the visibility rule, the state-only writes (which must not touch the
// content an admin cannot edit) and the uniqueness the catalog depends on.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

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

// ======== skills ========

func TestListSkills_ScopeAndOrder(t *testing.T) {
	s := storetest.Connect(t)

	for _, rec := range []store.SkillRecord{
		skillFixture("skill:z", "platform", "", "zeta"),
		skillFixture("skill:a", "platform", "", "alpha"),
		skillFixture("skill:m", "user", "u-1", "mine"),
		skillFixture("skill:t", "user", "u-2", "theirs"),
	} {
		seedSkillDoc(t, s, rec)
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

// ======== per-user side tables ========

// TestCapabilityVisible pins the resolution's own visibility check, used when an
// attachment is applied.
func TestCapabilityVisible(t *testing.T) {
	s := storetest.Connect(t)

	seedBuiltinTool(t, s, "shell")
	// Creating an MCP server is the admin module's write; this test only needs
	// the row visible to one owner.
	if _, err := s.DB().Collection("admin_mcp_servers").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: "mcp:mine"}, {Key: "scope", Value: "user"},
		{Key: "owner_user_id", Value: "u-1"}, {Key: "name", Value: "Mine"},
	}); err != nil {
		t.Fatalf("seed mcp server: %v", err)
	}
	seedSkillDoc(t, s, skillFixture("skill:platform", "platform", "", "shared"))

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

// seedSkillDoc writes a catalogue row directly: creating skills is the admin
// module's write now, and these tests only need the rows to exist.
func seedSkillDoc(t *testing.T, s *store.Store, rec store.SkillRecord) {
	t.Helper()
	if _, err := s.DB().Collection("admin_skills").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: rec.ID}, {Key: "scope", Value: rec.Scope},
		{Key: "owner_user_id", Value: rec.OwnerUserID}, {Key: "name", Value: rec.Name},
		{Key: "description", Value: rec.Description}, {Key: "files", Value: rec.Files},
		{Key: "default_enabled", Value: rec.DefaultEnabled}, {Key: "locked", Value: rec.Locked},
		{Key: "origin", Value: rec.Origin}, {Key: "version", Value: rec.Version},
		{Key: "status", Value: rec.Status}, {Key: "published_version", Value: rec.PublishedVersion},
		{Key: "content_hash", Value: rec.ContentHash},
		{Key: "created_at", Value: time.Now().UTC()}, {Key: "updated_at", Value: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("seed skill %s: %v", rec.ID, err)
	}
}
