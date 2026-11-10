package store_test

// These tests run against a real MongoDB (see internal/store/storetest). What
// they pin is server behaviour a driver mock could not observe: that the id
// sequence hands out one distinct value per append — the property an
// AUTO_INCREMENT column used to provide — and that "most recent first" survives
// the move.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func TestRecordAudit_AssignsIncreasingIDs(t *testing.T) {
	s := storetest.Connect(t)

	for _, action := range []string{"config.update", "agent.create", "agent.delete"} {
		if err := s.RecordAudit(store.AuditEntry{
			ActorID: "alice", ActorRole: "admin", Action: action,
			ResourceType: "config", ResourceID: "agent/foo", IP: "127.0.0.1",
		}); err != nil {
			t.Fatalf("RecordAudit: %v", err)
		}
	}

	entries, err := s.ListAudit(store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	// Most recent first, and the ids increase with insertion order.
	if entries[0].ID <= entries[1].ID || entries[1].ID <= entries[2].ID {
		t.Fatalf("ids are not increasing: %d %d %d", entries[0].ID, entries[1].ID, entries[2].ID)
	}
	if entries[0].ID == 0 {
		t.Fatal("an entry was stored without an id")
	}
}

// TestRecordAudit_IDsAreDistinctUnderConcurrency is what the counters
// collection exists for: a read-then-write counter hands the same id to two
// writers here, and a single atomic findOneAndUpdate cannot.
func TestRecordAudit_IDsAreDistinctUnderConcurrency(t *testing.T) {
	s := storetest.Connect(t)

	const writers = 20
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = s.RecordAudit(store.AuditEntry{Action: "agent.delete", ResourceType: "agent"})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	entries, err := s.ListAudit(store.AuditFilter{Limit: 500})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != writers {
		t.Fatalf("expected %d entries, got %d", writers, len(entries))
	}
	seen := make(map[int64]bool, writers)
	for _, e := range entries {
		if seen[e.ID] {
			t.Fatalf("id %d was handed out twice", e.ID)
		}
		seen[e.ID] = true
	}
}

func TestListAudit_DescribesTheEntry(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.RecordAudit(store.AuditEntry{
		ActorID: "alice", ActorRole: "admin", Action: "agent.delete",
		ResourceType: "agent", ResourceID: "a1", IP: "1.2.3.4",
		Detail: map[string]any{"k": "v", "nested": map[string]any{"n": float64(2)}},
	}); err != nil {
		t.Fatalf("RecordAudit: %v", err)
	}

	entries, err := s.ListAudit(store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Action != "agent.delete" || e.ActorID != "alice" || e.ActorRole != "admin" ||
		e.ResourceType != "agent" || e.ResourceID != "a1" || e.IP != "1.2.3.4" {
		t.Fatalf("round trip lost fields: %+v", e)
	}
	// Compared as JSON, not as Go types: the JSON is what the admin API returns
	// and the portal reads, while whether a nested document arrives as bson.M or
	// map[string]any is the driver's business. Without the collection's
	// DefaultDocumentM this is `{"k":"v","nested":[{"Key":"n","Value":2}]}`.
	encoded, err := json.Marshal(e.Detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	if string(encoded) != `{"k":"v","nested":{"n":2}}` {
		t.Fatalf("detail does not round trip as JSON: %s", encoded)
	}
	if e.CreatedAt.IsZero() {
		t.Fatal("created_at was not set")
	}
}

func TestListAudit_MostRecentFirstAndLimited(t *testing.T) {
	s := storetest.Connect(t)

	for _, action := range []string{"a", "b", "c"} {
		if err := s.RecordAudit(store.AuditEntry{Action: action}); err != nil {
			t.Fatalf("RecordAudit: %v", err)
		}
	}

	entries, err := s.ListAudit(store.AuditFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 2 || entries[0].Action != "c" || entries[1].Action != "b" {
		t.Fatalf("expected the two most recent [c b], got %+v", entries)
	}

	// A limit outside the accepted range falls back to the default rather than
	// returning everything.
	entries, err = s.ListAudit(store.AuditFilter{Limit: -1})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected the default limit to return all 3, got %d", len(entries))
	}
}

func TestListAudit_Empty(t *testing.T) {
	s := storetest.Connect(t)

	entries, err := s.ListAudit(store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %d", len(entries))
	}
}

func TestMigrations_DeclareAuditIndexes(t *testing.T) {
	s := storetest.Connect(t)

	// The list query sorts on (created_at desc, _id desc): both fields have to
	// be in the key, or the order stops being deterministic for entries written
	// within the same clock tick.
	assertIndexKeys(t, s.DB().Collection("admin_audit_log"), "created_at_desc", [][2]any{
		{"created_at", int32(-1)}, {"_id", int32(-1)},
	})
	// Alert rules are the admin module's collection now; its own manifest
	// declares that index and its own tests assert it.

	// The three single-field indexes the SQL table carried, kept because the
	// admin UI filters the audit trail by each of them.
	assertIndexKeys(t, s.DB().Collection("admin_audit_log"), "action", [][2]any{{"action", int32(1)}})
	assertIndexKeys(t, s.DB().Collection("admin_audit_log"), "actor_id", [][2]any{{"actor_id", int32(1)}})
	assertIndexKeys(t, s.DB().Collection("admin_audit_log"), "resource", [][2]any{
		{"resource_type", int32(1)}, {"resource_id", int32(1)},
	})
}

// assertIndexUnique checks that one named index is unique — a constraint rather
// than an optimisation, and not something the key-order check can see.
func assertIndexUnique(t *testing.T, coll *mongo.Collection, name string) {
	t.Helper()

	cur, err := coll.Indexes().List(context.Background())
	if err != nil {
		t.Fatalf("list indexes on %s: %v", coll.Name(), err)
	}
	type indexDoc struct {
		Name   string `bson:"name"`
		Unique bool   `bson:"unique"`
	}
	for cur.Next(context.Background()) {
		var doc indexDoc
		if err := cur.Decode(&doc); err != nil {
			t.Fatalf("decode index: %v", err)
		}
		if doc.Name != name {
			continue
		}
		if !doc.Unique {
			t.Fatalf("index %s on %s is not unique", name, coll.Name())
		}
		return
	}
	t.Fatalf("index %s is missing from the applied manifests on %s", name, coll.Name())
}

// assertIndexKeys checks that one named index exists with exactly the given key
// order. It lives here rather than in storetest because it asserts, and because
// the domain tests that need it are in this package (see runner_test.go).
func assertIndexKeys(t *testing.T, coll *mongo.Collection, name string, want [][2]any) {
	t.Helper()

	cur, err := coll.Indexes().List(context.Background())
	if err != nil {
		t.Fatalf("list indexes on %s: %v", coll.Name(), err)
	}
	// Decoded into a concrete type rather than bson.M: how the driver represents
	// a nested document is its business, and asserting on that would make this
	// fail for the wrong reason.
	type indexDoc struct {
		Name string `bson:"name"`
		Key  bson.D `bson:"key"`
	}

	for cur.Next(context.Background()) {
		var doc indexDoc
		if err := cur.Decode(&doc); err != nil {
			t.Fatalf("decode index: %v", err)
		}
		if doc.Name != name {
			continue
		}
		if len(doc.Key) != len(want) {
			t.Fatalf("index %s has %d keys, want %d: %+v", name, len(doc.Key), len(want), doc.Key)
		}
		for i, pair := range want {
			if doc.Key[i].Key != pair[0] || doc.Key[i].Value != pair[1] {
				t.Fatalf("index %s key %d = %v %v, want %v %v",
					name, i, doc.Key[i].Key, doc.Key[i].Value, pair[0], pair[1])
			}
		}
		return
	}
	t.Fatalf("index %s is missing from the applied manifests on %s", name, coll.Name())
}

// TestListAudit_ResolvesActorNames replaces the SQL's LEFT JOIN users: the console
// shows a person, not an id, and the name is resolved at read time so a renamed
// account is not frozen into history.
func TestListAudit_ResolvesActorNames(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 1)
	// SeedUser leaves the display name unset, so set one the way the console would.
	if _, err := s.DB().Collection("auth_users").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "u-1"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "display_name", Value: "Ada"}}}},
	); err != nil {
		t.Fatalf("set display name: %v", err)
	}

	for _, actor := range []string{"u-1", "ghost"} {
		if err := s.RecordAudit(store.AuditEntry{ActorID: actor, Action: "agent.delete"}); err != nil {
			t.Fatalf("RecordAudit: %v", err)
		}
	}

	entries, err := s.ListAudit(store.AuditFilter{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	names := map[string]string{}
	for _, e := range entries {
		names[e.ActorID] = e.ActorName
	}
	if names["u-1"] != "Ada" {
		t.Fatalf("the actor's name was not resolved: %+v", names)
	}
	// An actor the users collection does not know is not an error: the trail
	// outlives the account.
	if names["ghost"] != "" {
		t.Fatalf("expected no name for an unknown actor, got %q", names["ghost"])
	}
}

// TestRecordAudit_HealsACounterThatIsBehind pins the repair path for the failure
// P1 left behind: the audit collection was renamed (`audit_log` →
// `admin_audit_log`), the counter is keyed by name, so the new counter started
// again at 1 while the migrated rows kept their ids. Every insert then collided
// and, because audit writes are best-effort, nobody noticed.
func TestRecordAudit_HealsACounterThatIsBehind(t *testing.T) {
	s := storetest.Connect(t)
	ctx := context.Background()

	// Rows that already exist, and a counter that only knows about one of them.
	for _, id := range []int64{1, 2, 3} {
		if _, err := s.DB().Collection("admin_audit_log").InsertOne(ctx, bson.D{
			{Key: "_id", Value: id}, {Key: "action", Value: "legacy"}, {Key: "created_at", Value: time.Now().UTC()},
		}); err != nil {
			t.Fatalf("seed row %d: %v", id, err)
		}
	}
	if _, err := s.DB().Collection("sys_counters").UpdateOne(ctx,
		bson.D{{Key: "_id", Value: "admin_audit_log"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "seq", Value: int64(1)}}}},
		options.UpdateOne().SetUpsert(true),
	); err != nil {
		t.Fatalf("seed counter: %v", err)
	}

	if err := s.RecordAudit(store.AuditEntry{
		ActorID: "alice", ActorRole: "admin", Action: "agent.create",
		ResourceType: "agent", ResourceID: "agent-1",
	}); err != nil {
		t.Fatalf("RecordAudit must repair the counter and succeed: %v", err)
	}

	var doc struct {
		ID int64 `bson:"_id"`
	}
	if err := s.DB().Collection("admin_audit_log").FindOne(ctx,
		bson.D{{Key: "action", Value: "agent.create"}}).Decode(&doc); err != nil {
		t.Fatalf("read the new entry: %v", err)
	}
	if doc.ID != 4 {
		t.Fatalf("new entry id = %d, want 4 (one past the highest stored row)", doc.ID)
	}
	// The counter must now be past the data, so the next write is not a repair.
	if err := s.RecordAudit(store.AuditEntry{ActorID: "alice", ActorRole: "admin", Action: "agent.delete", ResourceType: "agent"}); err != nil {
		t.Fatalf("second write: %v", err)
	}
}
