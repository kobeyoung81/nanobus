package store_test

// Built-in tool defaults. The catalog row is a two-plane thing: the definition
// plane (this row) says whether a tool starts on, and each agent type may decide
// for itself. The binary used to announce no default at all, so a fresh
// installation started with every tool off; a tool added after that fix must
// still arrive off, which is why the default is declared per tool rather than
// assumed.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func TestSyncBuiltinTools_InsertUsesTheDeclaredDefault(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.SyncBuiltinTools([]nanoprotocol.BuiltinToolDef{
		{ID: "read", DefaultEnabled: true},
		{ID: "experimental", DefaultEnabled: false},
	}); err != nil {
		t.Fatalf("SyncBuiltinTools: %v", err)
	}

	tools, err := s.ListBuiltinTools()
	if err != nil {
		t.Fatalf("ListBuiltinTools: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range tools {
		got[tool.ID] = tool.DefaultEnabled
	}
	if !got["read"] {
		t.Errorf("read starts off; want on")
	}
	if got["experimental"] {
		t.Errorf("experimental starts on; want off")
	}
}

func TestSyncBuiltinTools_BackfillsLegacyRowsOnly(t *testing.T) {
	s := storetest.Connect(t)
	ctx := context.Background()

	// A row inserted by a binary that announced no default: no updated_at,
	// default_enabled left at zero.
	if _, err := s.DB().Collection("admin_builtin_tools").InsertOne(ctx, bson.D{
		{Key: "_id", Value: "bash"}, {Key: "description", Value: "old"},
		{Key: "default_enabled", Value: false}, {Key: "locked", Value: ""},
	}); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	// A row an administrator switched off: UpdateBuiltinState writes updated_at.
	off := false
	if err := s.SyncBuiltinTools([]nanoprotocol.BuiltinToolDef{{ID: "write", DefaultEnabled: true}}); err != nil {
		t.Fatalf("SyncBuiltinTools write: %v", err)
	}
	if err := s.UpdateBuiltinState("write", &off, nil); err != nil {
		t.Fatalf("UpdateBuiltinState: %v", err)
	}

	if err := s.SyncBuiltinTools([]nanoprotocol.BuiltinToolDef{
		{ID: "bash", DefaultEnabled: true},
		{ID: "write", DefaultEnabled: true},
	}); err != nil {
		t.Fatalf("SyncBuiltinTools: %v", err)
	}

	tools, err := s.ListBuiltinTools()
	if err != nil {
		t.Fatalf("ListBuiltinTools: %v", err)
	}
	state := map[string]bool{}
	for _, tool := range tools {
		state[tool.ID] = tool.DefaultEnabled
	}
	if !state["bash"] {
		t.Errorf("legacy row was not backfilled; want on")
	}
	if state["write"] {
		t.Errorf("administrator's off decision was overwritten")
	}

	// Sanity: the administrator row really does carry updated_at, which is what
	// the backfill keys on.
	var doc struct {
		UpdatedAt time.Time `bson:"updated_at"`
	}
	if err := s.DB().Collection("admin_builtin_tools").FindOne(ctx, bson.D{{Key: "_id", Value: "write"}}).Decode(&doc); err != nil {
		t.Fatalf("read write row: %v", err)
	}
	if doc.UpdatedAt.IsZero() {
		t.Errorf("UpdateBuiltinState did not stamp updated_at")
	}
}
