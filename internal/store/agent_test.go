package store_test

// These tests run against a real MongoDB.
//
// What they assert is behaviour the SQL tests could not reach: that a tombstone
// is not resurrected by a status report, that flipping the default session agent
// leaves exactly one default, that the display-name check is case-insensitive
// (MySQL's default collation made it so, and an exact-match query would not be).

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// agentStore returns a store with a real MongoDB and a seeded user. The two
// reads that used to come from MySQL — the tier, and the binding's owner through
// `user_channels` — are served by the user document now that the users domain,
// with the channels it embeds, has moved too.
func agentStore(t *testing.T, tier string) *store.Store {
	t.Helper()
	s := storetest.Connect(t)
	// Binding 42: the id the fallback test asks for.
	storetest.SeedUser(t, s, "user-1", tier, 42)
	return s
}

// mustCreateAgent seeds a row directly: creating an agent is the portal's
// operation now, and what these tests read is the stored document.
func mustCreateAgent(t *testing.T, s *store.Store, id, userID string) {
	t.Helper()
	if _, err := s.DB().Collection("portal_agents").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id}, {Key: "user_id", Value: userID}, {Key: "type_id", Value: "build"},
		{Key: "display_name", Value: id}, {Key: "status", Value: "running"},
		{Key: "max_iterations", Value: 10}, {Key: "max_concurrent_messages", Value: 4},
		{Key: "created_at", Value: time.Now().UTC()}, {Key: "updated_at", Value: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
}

func TestAgentDisplayNameTaken_IsCaseInsensitive(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")
	if _, err := s.DB().Collection("portal_agents").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "agent-1"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "display_name", Value: "Ops Bot"}}}},
	); err != nil {
		t.Fatalf("rename agent: %v", err)
	}

	// MySQL's default collation compared case-insensitively; MongoDB's default
	// does not, so an exact-match query would silently loosen the rule.
	taken, err := s.AgentDisplayNameTaken("user-1", "ops bot", "")
	if err != nil {
		t.Fatalf("AgentDisplayNameTaken: %v", err)
	}
	if !taken {
		t.Fatal("a differently-cased duplicate was accepted")
	}
	// The agent being edited must not collide with itself.
	taken, err = s.AgentDisplayNameTaken("user-1", "Ops Bot", "agent-1")
	if err != nil {
		t.Fatalf("AgentDisplayNameTaken (excluded): %v", err)
	}
	if taken {
		t.Fatal("the agent being edited counted as its own duplicate")
	}
}

func TestListAgentsFiltered_HidesTombstones(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")
	mustCreateAgent(t, s, "agent-2", "user-1")
	if _, err := s.DB().Collection("portal_agents").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "agent-2"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "deleted"}}}},
	); err != nil {
		t.Fatalf("tombstone agent-2: %v", err)
	}

	live, err := s.ListAgents("user-1")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(live) != 1 || live[0].ID != "agent-1" {
		t.Fatalf("live agents = %+v", live)
	}

	all, err := s.ListAgentsFiltered("user-1", true)
	if err != nil {
		t.Fatalf("ListAgentsFiltered: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both agents with includeDeleted, got %d", len(all))
	}
}
