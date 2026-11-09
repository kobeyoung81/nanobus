package store_test

// These tests run against a real MongoDB (see internal/store/storetest): what
// they have to pin is server behaviour — that seeding does not clobber an
// operator's value, that a setter does, that the unique index is really there —
// none of which a driver mock can observe. The tests they replace asserted the
// exact SQL string the code was told to send, which is true by construction.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func TestGetConfigItems_SortedAndScoped(t *testing.T) {
	s := storetest.Connect(t)

	if err := seedConfig(t, s, "nanosec", []store.ConfigItem{
		{Key: "model", Value: "claude-sonnet", ValueType: "string", Description: "LLM model name"},
		{Key: "max_tokens", Value: "4096", ValueType: "int", Description: "Max tokens per request"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// A different component must not leak into the result.
	if err := seedConfig(t, s, "gateway", []store.ConfigItem{
		{Key: "port", Value: "8080", ValueType: "int", Description: "Gateway port"},
	}); err != nil {
		t.Fatalf("seed gateway: %v", err)
	}

	items, err := s.GetConfigItems("nanosec")
	if err != nil {
		t.Fatalf("GetConfigItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items for nanosec, got %d: %+v", len(items), items)
	}
	// Sorted by config_key — max_tokens before model.
	if items[0].Key != "max_tokens" || items[1].Key != "model" {
		t.Fatalf("expected [max_tokens model], got [%s %s]", items[0].Key, items[1].Key)
	}
	if items[1].Value != "claude-sonnet" || items[1].Description != "LLM model name" {
		t.Fatalf("round trip lost fields: %+v", items[1])
	}
}

func TestGetConfigItems_Empty(t *testing.T) {
	s := storetest.Connect(t)

	items, err := s.GetConfigItems("nonexistent")
	if err != nil {
		t.Fatalf("GetConfigItems: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestGetConfigItem_NotFound(t *testing.T) {
	s := storetest.Connect(t)

	_, err := s.GetConfigItem("nanosec", "missing")
	if err == nil {
		t.Fatal("expected an error for a missing item")
	}
}

func TestConfigItems_UniquePerComponentAndKey(t *testing.T) {
	s := storetest.Connect(t)
	ctx := context.Background()
	coll := s.DB().Collection("admin_config")

	_, err := coll.InsertOne(ctx, bson.D{
		{Key: "component_type", Value: "nanosec"},
		{Key: "config_key", Value: "model"},
		{Key: "config_value", Value: "a"},
	})
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err = coll.InsertOne(ctx, bson.D{
		{Key: "component_type", Value: "nanosec"},
		{Key: "config_key", Value: "model"},
		{Key: "config_value", Value: "b"},
	})
	if err == nil {
		t.Fatal("a duplicate (component_type, config_key) was accepted: the unique index is missing")
	}

	// The same key under a different component is a different item.
	if _, err := coll.InsertOne(ctx, bson.D{
		{Key: "component_type", Value: "gateway"},
		{Key: "config_key", Value: "model"},
		{Key: "config_value", Value: "c"},
	}); err != nil {
		t.Fatalf("same key under another component must be allowed: %v", err)
	}
}

// TestMigrations_DeclareConfigIndexes pins the manifest itself, so an index is
// not quietly dropped from it. storetest applies the real manifests, so this
// also proves they apply cleanly.
func TestMigrations_DeclareConfigIndexes(t *testing.T) {
	s := storetest.Connect(t)

	cur, err := s.DB().Collection("admin_config").Indexes().List(context.Background())
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	// Decoded into a concrete type rather than bson.M: how the driver represents
	// a nested document is its business, and asserting on that would make this
	// test fail for the wrong reason.
	type indexDoc struct {
		Name   string `bson:"name"`
		Unique bool   `bson:"unique"`
		Key    bson.D `bson:"key"`
	}

	var found bool
	for cur.Next(context.Background()) {
		var doc indexDoc
		if err := cur.Decode(&doc); err != nil {
			t.Fatalf("decode index: %v", err)
		}
		if doc.Name != "component_key_unique" {
			continue
		}
		found = true
		if !doc.Unique {
			t.Fatalf("component_key_unique is not unique: %+v", doc)
		}
		if len(doc.Key) != 2 || doc.Key[0].Key != "component_type" || doc.Key[1].Key != "config_key" {
			t.Fatalf("component_key_unique has the wrong key order: %+v", doc.Key)
		}
	}
	if !found {
		t.Fatal("component_key_unique is missing from the applied manifests")
	}
}

func TestConfigItemsToMap(t *testing.T) {
	m := store.ConfigItemsToMap([]store.ConfigItem{
		{Key: "model", Value: "claude"},
		{Key: "temperature", Value: "0.7"},
	})
	if len(m) != 2 || m["model"] != "claude" || m["temperature"] != "0.7" {
		t.Fatalf("unexpected map: %+v", m)
	}
	if got := store.ConfigItemsToMap(nil); len(got) != 0 {
		t.Fatalf("expected an empty map for nil input, got %d entries", len(got))
	}
}

// TestListConfigComponents pins the admin index's source: only component types
// that actually have configuration, sorted (the SQL it replaces ended with
// ORDER BY component_type).
func TestListConfigComponents(t *testing.T) {
	s := storetest.Connect(t)

	if err := seedConfig(t, s, "gateway", []store.ConfigItem{{Key: "port", Value: "8080"}}); err != nil {
		t.Fatalf("seed gateway: %v", err)
	}
	if err := seedConfig(t, s, "agent", []store.ConfigItem{{Key: "model", Value: "m"}}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	// An empty component must not appear: Distinct reads component_type off
	// documents, and seeding nothing writes nothing.
	components, err := s.ListConfigComponents()
	if err != nil {
		t.Fatalf("ListConfigComponents: %v", err)
	}
	if len(components) != 2 || components[0] != "agent" || components[1] != "gateway" {
		t.Fatalf("expected [agent gateway], got %v", components)
	}
}

func TestListConfigComponents_Empty(t *testing.T) {
	s := storetest.Connect(t)

	components, err := s.ListConfigComponents()
	if err != nil {
		t.Fatalf("ListConfigComponents: %v", err)
	}
	if len(components) != 0 {
		t.Fatalf("expected no components, got %v", components)
	}
}

// seedConfig writes config documents directly. The bus no longer writes this
// collection — nanoadmin owns it — so these read-path tests set up their data
// the way any other writer in the platform would: an insert.
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
