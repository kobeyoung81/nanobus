package server

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// seedConfig writes config documents directly: the bus no longer writes this
// collection (nanoadmin owns it), so tests set up their data the way another
// writer would, with an insert.
func seedConfig(t *testing.T, s *store.Store, component string, items []store.ConfigItem) error {
	t.Helper()
	ctx := context.Background()
	coll := s.DB().Collection("admin_config")
	for _, item := range items {
		if _, err := coll.InsertOne(ctx, bson.D{
			{Key: "component_type", Value: component},
			{Key: "config_key", Value: item.Key},
			{Key: "config_value", Value: item.Value},
			{Key: "value_type", Value: item.ValueType},
			{Key: "description", Value: item.Description},
			// The console shows "last changed", so seeded documents carry the
			// same field the owning service writes.
			{Key: "updated_at", Value: time.Now().UTC()},
		}); err != nil {
			return err
		}
	}
	return nil
}

// setConfigValue overwrites (or creates) one item's value, matching what the
// admin module's upsert does for an existing key.
func setConfigValue(t *testing.T, s *store.Store, component, key, value string) error {
	t.Helper()
	ctx := context.Background()
	coll := s.DB().Collection("admin_config")
	_, err := coll.UpdateOne(ctx,
		bson.D{{Key: "component_type", Value: component}, {Key: "config_key", Value: key}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "config_value", Value: value},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
		// Upsert: the auth keypair tests set a value that may not exist yet.
		options.UpdateOne().SetUpsert(true),
	)
	return err
}
