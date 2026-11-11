package store_test

// Index assertions shared by the domain tests. They live here rather than in
// storetest because they assert, and because several domains need them: a
// manifest that forgets an index leaves production without it and nothing fails
// at startup.

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

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
// order.
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
