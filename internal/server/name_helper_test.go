package server

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// setName points one collection row at a display name, so the log filters can be
// exercised against names rather than ids.
func setName(t *testing.T, srv *Server, collection, id, name string) {
	t.Helper()
	if _, err := srv.store.DB().Collection(collection).UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "display_name", Value: name}}}},
	); err != nil {
		t.Fatalf("set name on %s/%s: %v", collection, id, err)
	}
}
