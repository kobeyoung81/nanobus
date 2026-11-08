package store_test

// The runner registry is the domain where the move simplifies the code: what
// took a row lock and three statements is one filter plus one $set. The tests
// below run against a real MongoDB so that the sweeper's filter is evaluated by
// the server that will evaluate it in production — a mock would only assert the
// filter the test itself wrote.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// storedRunnerDoc mirrors the document so a test can look at fields the store's
// API does not return.
type storedRunnerDoc struct {
	ID            string    `bson:"_id"`
	Hostname      string    `bson:"hostname"`
	DockerHost    string    `bson:"docker_host"`
	Status        string    `bson:"status"`
	LastHeartbeat time.Time `bson:"last_heartbeat"`
	RegisteredAt  time.Time `bson:"registered_at"`
}

func readRunner(t *testing.T, s *store.Store, id string) storedRunnerDoc {
	t.Helper()
	var doc storedRunnerDoc
	if err := s.DB().Collection("provisioner_runners").
		FindOne(context.Background(), bson.D{{Key: "_id", Value: id}}).Decode(&doc); err != nil {
		t.Fatalf("read runner %s: %v", id, err)
	}
	return doc
}

// seedRunner writes a runner document directly, which is the only way to give it
// a heartbeat in the past: the API always stamps "now".
func seedRunner(t *testing.T, s *store.Store, id, status string, heartbeat time.Time) {
	t.Helper()
	if _, err := s.DB().Collection("provisioner_runners").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id},
		{Key: "hostname", Value: id + ".host"},
		{Key: "docker_host", Value: "local"},
		{Key: "status", Value: status},
		{Key: "last_heartbeat", Value: heartbeat},
		{Key: "registered_at", Value: heartbeat},
	}); err != nil {
		t.Fatalf("seed runner %s: %v", id, err)
	}
}

func TestRegisterRunner_InsertsOnline(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.RegisterRunner("runner-1", "host-a", "local"); err != nil {
		t.Fatalf("RegisterRunner: %v", err)
	}

	doc := readRunner(t, s, "runner-1")
	if doc.Hostname != "host-a" || doc.DockerHost != "local" {
		t.Fatalf("registration did not store the runner: %+v", doc)
	}
	if doc.Status != "online" {
		t.Fatalf("a new runner is not online: %q", doc.Status)
	}
	if doc.RegisteredAt.IsZero() || doc.LastHeartbeat.IsZero() {
		t.Fatalf("timestamps were not set: %+v", doc)
	}
}

// TestRegisterRunner_KeepsRegistrationTime pins registered_at as "first seen":
// the SQL's ON DUPLICATE KEY branch updated hostname, docker_host, status and
// last_heartbeat, and left registered_at alone.
func TestRegisterRunner_KeepsRegistrationTime(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.RegisterRunner("runner-1", "host-a", "local"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	first := readRunner(t, s, "runner-1")

	// A redeployed runner comes back with a new host and a fresh heartbeat.
	time.Sleep(10 * time.Millisecond)
	if err := s.RegisterRunner("runner-1", "host-b", "remote"); err != nil {
		t.Fatalf("second register: %v", err)
	}

	doc := readRunner(t, s, "runner-1")
	if doc.Hostname != "host-b" || doc.DockerHost != "remote" {
		t.Fatalf("re-registration did not update the runner: %+v", doc)
	}
	if !doc.RegisteredAt.Equal(first.RegisteredAt) {
		t.Fatalf("registered_at moved on re-registration: %v → %v", first.RegisteredAt, doc.RegisteredAt)
	}
	if !doc.LastHeartbeat.After(first.LastHeartbeat) {
		t.Fatalf("last_heartbeat was not refreshed: %v → %v", first.LastHeartbeat, doc.LastHeartbeat)
	}
}

// TestRegisterRunner_RevivesAnOfflineRunner: a runner that was swept offline and
// then heartbeats again has to come back, or the fleet view keeps showing a
// ghost.
func TestRegisterRunner_RevivesAnOfflineRunner(t *testing.T) {
	s := storetest.Connect(t)
	seedRunner(t, s, "runner-1", "offline", time.Now().UTC().Add(-time.Hour))

	if err := s.RegisterRunner("runner-1", "host-a", "local"); err != nil {
		t.Fatalf("RegisterRunner: %v", err)
	}

	if doc := readRunner(t, s, "runner-1"); doc.Status != "online" {
		t.Fatalf("an offline runner did not come back online: %+v", doc)
	}
}

func TestUpdateRunnerHeartbeat_TouchesTimestamp(t *testing.T) {
	s := storetest.Connect(t)
	old := time.Now().UTC().Add(-time.Minute)
	seedRunner(t, s, "runner-1", "online", old)

	if err := s.UpdateRunnerHeartbeat("runner-1"); err != nil {
		t.Fatalf("UpdateRunnerHeartbeat: %v", err)
	}

	if doc := readRunner(t, s, "runner-1"); !doc.LastHeartbeat.After(old) {
		t.Fatalf("heartbeat was not updated: %+v", doc)
	}
}

// TestUpdateRunnerHeartbeat_UnknownRunnerIsSilent pins the SQL's behaviour: an
// UPDATE that matched nothing returned no error.
func TestUpdateRunnerHeartbeat_UnknownRunnerIsSilent(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.UpdateRunnerHeartbeat("nobody"); err != nil {
		t.Fatalf("UpdateRunnerHeartbeat on an unknown runner: %v", err)
	}
}

// TestMarkStaleRunnersOffline pins both halves of the sweeper's filter: the
// stale runner flips, the fresh one does not. A filter that compared the wrong
// direction would mark the whole fleet offline, and only a real server test
// shows it.
func TestMarkStaleRunnersOffline(t *testing.T) {
	s := storetest.Connect(t)
	seedRunner(t, s, "stale", "online", time.Now().UTC().Add(-store.RunnerStaleTimeout-time.Minute))
	seedRunner(t, s, "fresh", "online", time.Now().UTC())
	seedRunner(t, s, "already-offline", "offline", time.Now().UTC().Add(-time.Hour))

	if err := s.MarkStaleRunnersOffline(); err != nil {
		t.Fatalf("MarkStaleRunnersOffline: %v", err)
	}

	if doc := readRunner(t, s, "stale"); doc.Status != "offline" {
		t.Fatalf("a stale runner is still online: %+v", doc)
	}
	if doc := readRunner(t, s, "fresh"); doc.Status != "online" {
		t.Fatalf("a fresh runner was marked offline: %+v", doc)
	}
	if doc := readRunner(t, s, "already-offline"); doc.Status != "offline" {
		t.Fatalf("an offline runner changed on its own: %+v", doc)
	}
	// It sweeps, it never deletes.
	if n, err := s.DB().Collection("provisioner_runners").CountDocuments(context.Background(), bson.D{}); err != nil || n != 3 {
		t.Fatalf("expected all 3 runners to survive the sweep, got %d (err %v)", n, err)
	}
}

func TestDeleteRunner(t *testing.T) {
	s := storetest.Connect(t)
	seedRunner(t, s, "runner-1", "online", time.Now().UTC())

	if err := s.DeleteRunner("runner-1"); err != nil {
		t.Fatalf("DeleteRunner: %v", err)
	}

	n, err := s.DB().Collection("provisioner_runners").CountDocuments(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("count runners: %v", err)
	}
	if n != 0 {
		t.Fatalf("runner survived the delete: %d documents", n)
	}
	// Deleting it again is not an error, as with the SQL DELETE.
	if err := s.DeleteRunner("runner-1"); err != nil {
		t.Fatalf("DeleteRunner on a missing runner: %v", err)
	}
}

// TestMigrations_DeclareRunnerIndexes pins the manifest against the queries the
// two readers actually run: the sweeper filters (status, last_heartbeat), and
// the fleet list sorts by registered_at descending.
// assertIndexKeys lives in audit_test.go, in this package.
func TestMigrations_DeclareRunnerIndexes(t *testing.T) {
	s := storetest.Connect(t)

	assertIndexKeys(t, s.DB().Collection("provisioner_runners"), "status_last_heartbeat", [][2]any{
		{"status", int32(1)}, {"last_heartbeat", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("provisioner_runners"), "registered_at_desc", [][2]any{
		{"registered_at", int32(-1)},
	})
}
