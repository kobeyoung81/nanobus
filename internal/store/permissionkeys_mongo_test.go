package store_test

// The permission key catalog moved from a SQL table (seeded by migrations) into
// the store itself, seeded from the binary. What matters is that a fresh
// installation gets the catalog, that an admin's edit survives every deploy, and
// that the catalog the binary ships is self-consistent — every read-only key it
// declares can be re-allowed after a deny-all.

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/permissionkeys"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func TestSeedPermissionKeys_InsertsAndIsIdempotent(t *testing.T) {
	s := storetest.Connect(t)

	keys := permissionkeys.Keys()
	inserted, err := s.SeedPermissionKeys(keys)
	if err != nil {
		t.Fatalf("SeedPermissionKeys: %v", err)
	}
	if inserted != len(keys) {
		t.Fatalf("inserted %d of %d keys", inserted, len(keys))
	}

	again, err := s.SeedPermissionKeys(keys)
	if err != nil {
		t.Fatalf("second SeedPermissionKeys: %v", err)
	}
	if again != 0 {
		t.Fatalf("the second run inserted %d keys", again)
	}

	stored, err := s.ListPermissionKeys()
	if err != nil {
		t.Fatalf("ListPermissionKeys: %v", err)
	}
	if len(stored) != len(keys) {
		t.Fatalf("stored %d keys, want %d", len(stored), len(keys))
	}
	// Ordered by id, and each key keeps what the catalog said.
	byID := map[string]nanoprotocol.PermissionKeyDef{}
	for _, k := range stored {
		byID[k.ID] = k
	}
	if byID["bash"].DefaultAction != nanoprotocol.PermissionAsk || byID["bash"].ReadOnly {
		t.Fatalf("unexpected key: %+v", byID["bash"])
	}
	if byID["read"].DefaultAction != nanoprotocol.PermissionAllow || !byID["read"].ReadOnly {
		t.Fatalf("unexpected key: %+v", byID["read"])
	}
	for i := 1; i < len(stored); i++ {
		if stored[i-1].ID >= stored[i].ID {
			t.Fatalf("keys are not in id order: %v", stored)
		}
	}
}

// TestSeedPermissionKeys_KeepsAdminEdits: the portal edits a key's description and
// default action, and a deploy must not undo that.
func TestSeedPermissionKeys_KeepsAdminEdits(t *testing.T) {
	s := storetest.Connect(t)
	if _, err := s.SeedPermissionKeys(permissionkeys.Keys()); err != nil {
		t.Fatalf("SeedPermissionKeys: %v", err)
	}

	// The admin loosens bash to the catalog default and rewrites its text.
	if _, err := s.DB().Collection("permission_keys").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "bash"}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "description", Value: "Run shell commands (ours)"},
			{Key: "default_action", Value: nanoprotocol.PermissionAllow},
		}}},
	); err != nil {
		t.Fatalf("edit key: %v", err)
	}

	if _, err := s.SeedPermissionKeys(permissionkeys.Keys()); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	stored, err := s.ListPermissionKeys()
	if err != nil {
		t.Fatalf("ListPermissionKeys: %v", err)
	}
	for _, k := range stored {
		if k.ID != "bash" {
			continue
		}
		if k.Description != "Run shell commands (ours)" || k.DefaultAction != nanoprotocol.PermissionAllow {
			t.Fatalf("the deploy undid the admin's edit: %+v", k)
		}
		return
	}
	t.Fatal("bash is missing from the catalog")
}

// TestSeedPermissionKeys_SkipsEmptyIDs: a malformed entry must not become a
// document whose _id is the empty string.
func TestSeedPermissionKeys_SkipsEmptyIDs(t *testing.T) {
	s := storetest.Connect(t)

	inserted, err := s.SeedPermissionKeys([]nanoprotocol.PermissionKeyDef{{Description: "no id"}})
	if err != nil {
		t.Fatalf("SeedPermissionKeys: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("an entry without an id was inserted")
	}
}

// TestShippedCatalog_CanReallowEveryReadOnlyKey is the consistency check the
// catalog has to pass: a deny-all policy is only acceptable when every read-only
// key is re-allowed after it (last match wins), which is exactly the shape the
// restricted agent types use. If a key were read-only and could not be
// re-allowed, no such policy could ever be saved.
func TestShippedCatalog_CanReallowEveryReadOnlyKey(t *testing.T) {
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)

	rules := []nanoprotocol.PermissionRule{{Pattern: "*", Action: nanoprotocol.PermissionDeny}}
	readOnly := 0
	for _, k := range permissionkeys.Keys() {
		if !k.ReadOnly {
			continue
		}
		readOnly++
		rules = append(rules, nanoprotocol.PermissionRule{Pattern: k.ID, Action: nanoprotocol.PermissionAllow})
	}
	if readOnly == 0 {
		t.Fatal("the shipped catalog declares no read-only keys")
	}

	if err := s.SaveAgentType(nanoprotocol.AgentTypeDef{
		ID: "restricted", Name: "restricted", Permissions: rules,
	}); err != nil {
		t.Fatalf("a deny-all policy with every read-only key re-allowed was rejected: %v", err)
	}

	// And without the re-allows it is refused — that is what read-only means.
	if err := s.SaveAgentType(nanoprotocol.AgentTypeDef{
		ID: "restricted", Name: "restricted",
		Permissions: []nanoprotocol.PermissionRule{{Pattern: "*", Action: nanoprotocol.PermissionDeny}},
	}); err == nil {
		t.Fatal("a deny-all policy that moves the read-only keys was accepted")
	}
}
