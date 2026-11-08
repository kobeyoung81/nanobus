package store_test

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	migrationsmongo "gitee.com/kobeyoung81/nanobus/migrations-mongo"
)

// The bus and nanoartifacts keep their migration bookkeeping in one shared
// collection, so the record id has to say which service it belongs to. If the
// service were left out, whichever service migrated first would mark the
// other's manifests as applied — silently, because a filename is all either
// side ever compares. Both sides name their files with a numeric prefix, so
// the collision below is the normal case, not a freak one.
func TestMigrationsAreScopedToTheBus(t *testing.T) {
	s := storetest.Connect(t)
	ctx := context.Background()

	foreign := bson.D{
		{Key: "_id", Value: "artifacts:001_service_config_items.json"},
		{Key: "service", Value: "artifacts"},
	}
	if _, err := s.DB().Collection("sys_migrations").InsertOne(ctx, foreign); err != nil {
		t.Fatalf("insert foreign migration record: %v", err)
	}

	manifests, err := migrationsmongo.Load()
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	status, err := s.MigrationStatus(manifests)
	if err != nil {
		t.Fatalf("migration status: %v", err)
	}
	if len(status) != len(manifests) {
		t.Fatalf("status covers %d manifests, want %d", len(status), len(manifests))
	}
	for _, m := range status {
		if !m.Applied {
			t.Errorf("%s reported pending: the foreign record was read as this service's", m.Name)
		}
	}

	own, err := s.DB().Collection("sys_migrations").CountDocuments(ctx,
		bson.D{{Key: "service", Value: "bus"}})
	if err != nil {
		t.Fatalf("count bus records: %v", err)
	}
	if own != int64(len(manifests)) {
		t.Fatalf("bus records = %d, want %d", own, len(manifests))
	}

	var record bson.M
	if err := s.DB().Collection("sys_migrations").FindOne(ctx,
		bson.D{{Key: "service", Value: "bus"}}).Decode(&record); err != nil {
		t.Fatalf("read a bus record: %v", err)
	}
	if id, _ := record["_id"].(string); !strings.HasPrefix(id, "bus:") {
		t.Fatalf("record id = %q, want a bus: prefix", id)
	}
}

// Indexes live in the manifests, and this transition renamed both the
// manifests and the collections they describe. A record whose name no longer
// matches its manifest would re-apply that migration on every start.
func TestMigrationRecordNamesMatchTheManifests(t *testing.T) {
	s := storetest.Connect(t)
	ctx := context.Background()

	manifests, err := migrationsmongo.Load()
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	cur, err := s.DB().Collection("sys_migrations").Find(ctx,
		bson.D{{Key: "service", Value: "bus"}},
		options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		t.Fatalf("find records: %v", err)
	}
	seen := map[string]bool{}
	for cur.Next(ctx) {
		var doc struct {
			ID string `bson:"_id"`
		}
		if err := cur.Decode(&doc); err != nil {
			t.Fatalf("decode record: %v", err)
		}
		seen[strings.TrimPrefix(doc.ID, "bus:")] = true
	}
	if err := cur.Err(); err != nil {
		t.Fatalf("iterate records: %v", err)
	}
	for name := range manifests {
		if !seen[name] {
			t.Errorf("manifest %s has no applied record", name)
		}
	}
}
