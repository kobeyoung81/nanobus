package store_test

// ResolveCapabilities assembles the catalog one mode sees: builtin tools, MCP
// servers, skills and models, all for the agent's owner. The visibility rule it
// rests on now spans four collections that all live in MongoDB, so this seeds
// real documents — what it asserts is that another user's MCP server and skill
// never turn up in someone else's catalog.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// seedAgentDoc writes the row the resolution reads; creating an agent is the
// portal's operation now.
func seedAgentDoc(t *testing.T, s *store.Store, id, userID, typeID string) {
	t.Helper()
	if _, err := s.DB().Collection("portal_agents").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id}, {Key: "user_id", Value: userID}, {Key: "type_id", Value: typeID},
		{Key: "display_name", Value: id}, {Key: "status", Value: "running"},
		{Key: "created_at", Value: time.Now().UTC()}, {Key: "updated_at", Value: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
}

// seedTypeDoc writes a minimal agent type: saving one is the admin module's
// operation now, and the resolution only reads it.
func seedTypeDoc(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if _, err := s.DB().Collection("admin_agent_types").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id}, {Key: "name", Value: id}, {Key: "role", Value: "general"},
		{Key: "strategy", Value: "react"}, {Key: "context", Value: "windowed"},
		{Key: "output", Value: "stream"}, {Key: "session_binding", Value: "shared"},
	}); err != nil {
		t.Fatalf("seed type %s: %v", id, err)
	}
}

// seedSkillAck writes the acknowledgement row the resolution reads.
func seedSkillAck(t *testing.T, s *store.Store, userID, skillID string, version int) {
	t.Helper()
	if _, err := s.DB().Collection("admin_user_skill_acks").UpdateOne(context.Background(),
		bson.D{{Key: "user_id", Value: userID}, {Key: "skill_id", Value: skillID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "acked_version", Value: version}}}},
		options.UpdateOne().SetUpsert(true)); err != nil {
		t.Fatalf("seed skill ack: %v", err)
	}
}

func TestResolveCapabilities_VisibleSkipsOtherUsers(t *testing.T) {
	// The type domain is in MongoDB too, and a user's catalog is resolved against
	// their mode's definition — so the mode (and the permission catalog a save
	// validates against) has to exist.
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 1)
	seedTypeDoc(t, s, "build")

	seedAgentDoc(t, s, "agent-1", "u-1", "build")
	seedBuiltinTool(t, s, "shell")

	// Creating catalog entries is the admin module's write; these rows only have
	// to be visible to the owner or to everyone.
	for _, row := range []struct{ id, scope, owner, name string }{
		{"mcp:global", "platform", "", "global"},
		{"mcp:mine", "user", "u-1", "mine"},
		{"mcp:theirs", "user", "u-2", "theirs"},
	} {
		if _, err := s.DB().Collection("admin_mcp_servers").InsertOne(context.Background(), bson.D{
			{Key: "_id", Value: row.id}, {Key: "scope", Value: row.scope},
			{Key: "owner_user_id", Value: row.owner}, {Key: "name", Value: row.name},
		}); err != nil {
			t.Fatalf("seed mcp server %s: %v", row.id, err)
		}
	}
	for _, rec := range []store.SkillRecord{
		skillFixture("skill:platform", "platform", "", "shared"),
		skillFixture("skill:mine", "user", "u-1", "mine"),
		skillFixture("skill:theirs", "user", "u-2", "theirs"),
	} {
		seedSkillDoc(t, s, rec)
	}

	resp, err := s.ResolveCapabilities("agent-1")
	if err != nil {
		t.Fatalf("ResolveCapabilities: %v", err)
	}

	mcpIDs := map[string]bool{}
	for _, m := range resp.McpServers {
		mcpIDs[m.ID] = true
	}
	if mcpIDs["mcp:theirs"] {
		t.Fatalf("another user's mcp server leaked into the resolve: %+v", resp.McpServers)
	}
	if !mcpIDs["mcp:mine"] || !mcpIDs["mcp:global"] {
		t.Fatalf("expected the own and platform servers, got %+v", resp.McpServers)
	}

	skillIDs := map[string]bool{}
	for _, sk := range resp.Skills {
		skillIDs[sk.ID] = true
	}
	if skillIDs["skill:theirs"] {
		t.Fatalf("another user's skill leaked into the resolve: %+v", resp.Skills)
	}
	if !skillIDs["skill:mine"] || !skillIDs["skill:platform"] {
		t.Fatalf("expected the own and platform skills, got %+v", resp.Skills)
	}

	if len(resp.BuiltinTools) != 1 || resp.BuiltinTools[0].ID != "shell" {
		t.Fatalf("builtin tools missing from the resolve: %+v", resp.BuiltinTools)
	}
	// The mode's own definition is resolved alongside its catalog.
	if resp.AgentType == nil || resp.AgentType.ID != "build" {
		t.Fatalf("the agent type is missing from the resolve: %+v", resp.AgentType)
	}
}

// TestResolveCapabilities_SkillUpdateAvailability: a platform skill the user has
// acknowledged below its published version is flagged, which is what the portal's
// "update available" badge reads.
func TestResolveCapabilities_SkillUpdateAvailability(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 1)
	seedAgentDoc(t, s, "agent-1", "u-1", "build")

	// The bundled-id format, spelled here: the seeder that defines it is the
	// admin module's now, and the read only needs a row that looks like its output.
	id := "skill:bundled-weather"
	seedSkillDoc(t, s, store.SkillRecord{
		Capability: store.Capability{ID: id, Scope: "platform", Name: "weather", DefaultEnabled: true},
		Files:      map[string]string{"SKILL.md": "body"},
		Origin:     "bundled", Status: "published", Version: 2, PublishedVersion: 2,
	})

	// Unacknowledged: the badge is not shown for a version never seen.
	resp, err := s.ResolveCapabilities("agent-1")
	if err != nil {
		t.Fatalf("ResolveCapabilities: %v", err)
	}
	if len(resp.Skills) != 1 {
		t.Fatalf("expected one skill, got %+v", resp.Skills)
	}
	if resp.Skills[0].AckedVersion != 0 || resp.Skills[0].UpdateAvailable {
		t.Fatalf("unexpected flags before an ack: %+v", resp.Skills[0])
	}

	// Acknowledged at 1, published 2 → an update is available. The ack is the
	// admin module's row; this test only needs it to exist.
	seedSkillAck(t, s, "u-1", id, 1)
	resp, err = s.ResolveCapabilities("agent-1")
	if err != nil {
		t.Fatalf("ResolveCapabilities: %v", err)
	}
	if resp.Skills[0].AckedVersion != 1 || !resp.Skills[0].UpdateAvailable {
		t.Fatalf("expected an update to be flagged: %+v", resp.Skills[0])
	}

	// Acknowledged at the published version → nothing to update.
	seedSkillAck(t, s, "u-1", id, 2)
	resp, err = s.ResolveCapabilities("agent-1")
	if err != nil {
		t.Fatalf("ResolveCapabilities: %v", err)
	}
	if resp.Skills[0].UpdateAvailable {
		t.Fatalf("an up-to-date skill was flagged: %+v", resp.Skills[0])
	}
}
