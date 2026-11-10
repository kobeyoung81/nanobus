package store_test

// The sidebar's order, as the bus reads it: which agents sit in which group and
// in what order. The arrange call is the portal's now; the read stays because the
// console's order query is served from here.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func orderAgent(t *testing.T, s *store.Store, id, userID, groupID string, position int64, status string) {
	t.Helper()
	if _, err := s.DB().Collection("portal_agents").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id}, {Key: "user_id", Value: userID}, {Key: "type_id", Value: "build"},
		{Key: "group_id", Value: groupID}, {Key: "display_name", Value: id},
		{Key: "status", Value: status}, {Key: "position", Value: position},
		{Key: "created_at", Value: time.Now().UTC()}, {Key: "updated_at", Value: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestListAgentOrderSkipsDeletedAgents(t *testing.T) {
	s := storetest.Connect(t)
	orderAgent(t, s, "a1", "user-1", "g1", 1, "running")
	orderAgent(t, s, "a2", "user-1", "g1", 2, "deleted")
	orderAgent(t, s, "a3", "user-1", "", 3, "running")

	groups, ungrouped, err := s.ListAgentOrder("user-1")
	if err != nil {
		t.Fatalf("ListAgentOrder: %v", err)
	}
	if !sameIDs(groups["g1"], []string{"a1"}) {
		t.Fatalf("a deleted agent is still listed: %v", groups["g1"])
	}
	if !sameIDs(ungrouped, []string{"a3"}) {
		t.Fatalf("ungrouped = %v, want [a3]", ungrouped)
	}
}
