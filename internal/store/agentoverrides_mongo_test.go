package store_test

// The override document moved with the catalogue: nanoadmin owns its writes, and
// their semantics (replace-not-merge, the read-only key rule, the pair primary
// key) are asserted next door. What is left here is the side the bus still
// serves — the reads the agent's configuration snapshot is assembled from — and
// the manifest that declares the collection, which still lives in this module.

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// seedOverride writes one override document the way the admin module does, so the
// bus's read paths can be tested against stored data without a write method it no
// longer has.
func seedOverride(t *testing.T, s *store.Store, agentID, typeID string, fields bson.D) {
	t.Helper()
	doc := bson.D{{Key: "agent_id", Value: agentID}, {Key: "type_id", Value: typeID}}
	doc = append(doc, fields...)
	if _, err := s.DB().Collection("admin_agent_type_overrides").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed override: %v", err)
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

func TestListAgentTypeOverrides_KeyedByType(t *testing.T) {
	s := storetest.Connect(t)

	seedOverride(t, s, "agent-1", "build", bson.D{{Key: "thinking", Value: "high"}})
	seedOverride(t, s, "agent-1", "plan", bson.D{{Key: "thinking", Value: "low"}})
	seedOverride(t, s, "agent-2", "build", bson.D{{Key: "thinking", Value: "medium"}})

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

	seedOverride(t, s, "agent-1", "build", bson.D{{Key: "thinking", Value: "high"}})
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

// TestMigrations_DeclareOverrideIndexes pins the manifest, including the unique
// pair the SQL primary key covered. The document's writer is the admin module;
// the declaration has not moved yet, so this asserts it where it is.
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
	ctx := context.Background()

	if _, err := s.DB().Collection("admin_agent_types").InsertOne(ctx, bson.D{
		{Key: "_id", Value: "build"},
		{Key: "name", Value: "build"},
		{Key: "role", Value: "general"},
		{Key: "settings", Value: bson.D{
			{Key: "session_mode", Value: "continuous"},
			{Key: "shell_timeout", Value: "30s"},
		}},
	}); err != nil {
		t.Fatalf("seed type: %v", err)
	}
	seedOverride(t, s, "agent-1", "build", bson.D{{Key: "settings", Value: bson.D{
		{Key: "shell_timeout", Value: "60s"},
	}}})

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
