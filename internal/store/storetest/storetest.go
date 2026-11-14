// Package storetest gives tests a Store backed by a real MongoDB.
//
// It used to hand back a MySQL handle as well, while the domains were split
// across the two stores. With the last domain moved there is only one store to
// build, so a test that needs "a store" needs nothing else.
//
// The store moved from MySQL to MongoDB, and mocking the driver would prove
// almost nothing: what the store relies on — unique indexes rejecting a second
// document, upserts being atomic, $push appending in place, findOneAndUpdate
// claiming a job exactly once — are properties of the *server*, not of the call
// shape a mock would assert. So tests run against a real server, and skip when
// none is configured (which keeps a plain `go test ./...` working on a machine
// without one):
//
//	MONGO_TEST_URI='mongodb://user:pass@host:27017/?authSource=admin' go test ./...
//
// Each call gets its own database, dropped on cleanup: tests cannot see each
// other's documents, and a failed run leaves nothing behind. The real migration
// manifests are applied, so the indexes under test are the ones production gets
// rather than a hand-rolled approximation.
package storetest

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	migrationsmongo "gitee.com/kobeyoung81/nanobus/migrations-mongo"
)

// URI returns the MongoDB URI tests should use, preferring MONGO_TEST_URI so a
// test run can point somewhere other than the deployment.
func URI() string {
	if v := os.Getenv("MONGO_TEST_URI"); v != "" {
		return v
	}
	return os.Getenv("MONGO_URI")
}

// Connect returns a Store backed by a fresh, migrated database, dropped when the
// test ends. The test is skipped — not failed — when no URI is configured.
func Connect(t *testing.T) *store.Store {
	t.Helper()
	client, s := connectMongo(t)
	_ = client
	return s
}

// connectMongo sets up the per-test database and returns both the client (for
// the caller that needs to wrap it again) and a Mongo-only Store.
func connectMongo(t *testing.T) (*mongo.Client, *store.Store) {
	t.Helper()

	uri := URI()
	if uri == "" {
		t.Skip("MONGO_TEST_URI/MONGO_URI not set: skipping tests that need a real MongoDB")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("connect mongo: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatalf("ping mongo (%s): %v", uri, err)
	}

	name := databaseName(t.Name())

	// A leftover from an interrupted run would otherwise carry documents into
	// this one, and the unique indexes under test would fire on the wrong data.
	if err := client.Database(name).Drop(ctx); err != nil {
		t.Fatalf("drop stale test database %s: %v", name, err)
	}

	s := store.NewMongo(client, name)
	manifests, err := migrationsmongo.Load()
	if err != nil {
		t.Fatalf("load migration manifests: %v", err)
	}
	if _, err := s.Migrate(manifests); err != nil {
		t.Fatalf("apply migration manifests: %v", err)
	}

	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if err := client.Database(name).Drop(dropCtx); err != nil {
			t.Logf("drop test database %s: %v", name, err)
		}
		if err := client.Disconnect(dropCtx); err != nil {
			t.Logf("disconnect: %v", err)
		}
	})

	return client, s
}

// databaseName derives a per-test database name, at most 63 bytes.
//
// MongoDB rejects database names containing /\."$*<>:|? and spaces, and refuses
// any longer than 63 bytes — while subtests produce names full of slashes and
// long enough to blow that limit on their own. So everything outside [a-z0-9_] is
// folded away and the result is truncated *after* the unique suffix is appended,
// since truncating before it can cut the suffix off entirely.
func databaseName(testName string) string {
	const maxLen = 63

	suffix := fmt.Sprintf("_%d", time.Now().UnixNano()%1_000_000)
	var b strings.Builder
	b.WriteString("nanobus_test_")
	for _, r := range strings.ToLower(testName) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	limit := maxLen - len(suffix)
	name := b.String()
	if len(name) > limit {
		name = name[:limit]
	}
	// Uniqueness matters more than readability: two tests sharing a database
	// would fail each other intermittently.
	return name + suffix
}

// Client returns a connected client and the name of a fresh, already-migrated
// database, for a test that must build its own Store — typically because it
// needs the client itself (to read a collection the store does not expose).
func Client(t *testing.T) (*mongo.Client, string) {
	t.Helper()
	client, s := connectMongo(t)
	return client, s.DB().Name()
}

// SeedUser inserts a user document with one linked channel — the shape the store
// writes — so a test that needs a tier, a binding owner or a display name has
// something to read.
func SeedUser(t *testing.T, s *store.Store, userID, tier string, bindingID int64) {
	t.Helper()
	SeedUserChannel(t, s, userID, tier, "test", "sender-"+userID, bindingID)
}

// SeedUserChannel is SeedUser with the identity spelled out, for a test whose
// request names a channel of its own.
func SeedUserChannel(t *testing.T, s *store.Store, userID, tier, channel, senderID string, bindingID int64) {
	t.Helper()
	// When the store is wired to the in-memory account backend, the seeded account
	// belongs there: the bus no longer keeps `auth_users` itself.
	if fake, ok := s.AccountBackend().(*FakeAccounts); ok {
		fake.Seed(userID, tier, channel, senderID, bindingID)
		return
	}
	now := time.Now().UTC()
	if _, err := s.DB().Collection("auth_users").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: userID},
		{Key: "status", Value: "active"},
		{Key: "tier", Value: tier},
		{Key: "created_at", Value: now},
		{Key: "updated_at", Value: now},
		{Key: "channels", Value: bson.A{bson.D{
			{Key: "binding_id", Value: bindingID},
			{Key: "channel", Value: channel},
			{Key: "sender_id", Value: senderID},
			{Key: "enabled", Value: true},
			{Key: "binding_status", Value: "active"},
			{Key: "created_at", Value: now},
		}}},
	}); err != nil {
		t.Fatalf("seed user %s: %v", userID, err)
	}
}
