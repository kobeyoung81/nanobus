package store_test

// The provisioning queue's read side, which is still the bus's: the console and the
// runtime snapshot list container requests. The account half of what used to live
// next to it moved to the auth service with its tests.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// seedContainerRequest writes a queue row directly: the provisioner is the writer
// and this module only reads.
func seedContainerRequest(t *testing.T, s *store.Store, userID string, bindingID *int64, agentID, level, status string, createdAt time.Time) {
	t.Helper()
	doc := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "agent_id", Value: agentID},
		{Key: "level", Value: level},
		{Key: "status", Value: status},
		{Key: "created_at", Value: createdAt},
		{Key: "updated_at", Value: createdAt},
	}
	if bindingID != nil {
		doc = append(doc, bson.E{Key: "binding_id", Value: *bindingID})
	}
	if _, err := s.DB().Collection("provisioner_container_requests").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed container request for %s: %v", userID, err)
	}
}

func TestListContainers_NewestFirst(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)

	base := time.Now().UTC()
	seedContainerRequest(t, s, "u-1", nil, "a", "free", "disposed", base.Add(-2*time.Hour))
	seedContainerRequest(t, s, "u-1", nil, "b", "free", "running", base.Add(-time.Hour))
	seedContainerRequest(t, s, "u-1", nil, "c", "free", "pending", base)

	requests, err := s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(requests))
	}
	if requests[0].AgentID != "c" || requests[2].AgentID != "a" {
		t.Fatalf("expected newest first, got %+v", requests)
	}

	limited, err := s.ListContainers("u-1", 1)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(limited) != 1 || limited[0].AgentID != "c" {
		t.Fatalf("limit not honoured: %+v", limited)
	}
}
