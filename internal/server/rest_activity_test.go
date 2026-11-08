package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// The runtime snapshot's agent list and the user list's "last seen" column. Both
// were SELECTs against MySQL — `agents` and the two queues — and both are the
// bus's now.

// TestRestListAgentFleet is the runtime snapshot's agent list: everybody's agents,
// newest first, each with its owner.
func TestRestListAgentFleet(t *testing.T) {
	srv := testServerMongo(t)
	base := time.Now().UTC()
	for _, a := range []struct{ id, user string }{{"agent-1", "u-1"}, {"agent-2", "u-2"}} {
		if _, err := srv.store.DB().Collection("agents").InsertOne(context.Background(), bson.D{
			{Key: "_id", Value: a.id},
			{Key: "user_id", Value: a.user},
			{Key: "type_id", Value: "build"},
			{Key: "display_name", Value: a.id},
			{Key: "status", Value: "running"},
			{Key: "created_at", Value: base.Add(-2 * time.Hour)},
			{Key: "updated_at", Value: base.Add(-time.Hour)},
		}); err != nil {
			t.Fatalf("seed agent %s: %v", a.id, err)
		}
	}
	// The newer one sorts first.
	if _, err := srv.store.DB().Collection("agents").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "agent-2"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "updated_at", Value: base}}}},
	); err != nil {
		t.Fatalf("touch agent-2: %v", err)
	}

	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/agent-fleet?limit=10", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Agents []struct {
			ID          string `json:"id"`
			UserID      string `json:"user_id"`
			DisplayName string `json:"display_name"`
			Status      string `json:"status"`
			UpdatedAt   string `json:"updated_at"`
		} `json:"agents"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Agents) != 2 || body.Count != 2 {
		t.Fatalf("expected two agents, got %+v", body)
	}
	if body.Agents[0].ID != "agent-2" || body.Agents[0].UserID != "u-2" {
		t.Fatalf("unexpected order: %+v", body.Agents)
	}
	if body.Agents[0].DisplayName != "agent-2" || body.Agents[0].UpdatedAt == "" {
		t.Fatalf("unexpected agent: %+v", body.Agents[0])
	}
}

// TestRestUserActivity is the user list's "last seen": the newest message either
// direction, per user. It used to be two MySQL aggregates the portal merged itself.
func TestRestUserActivity(t *testing.T) {
	srv := testServerMongo(t)
	// Truncated to the second: the endpoint answers RFC3339, so a sub-second
	// instant would not survive the round trip and the comparison below would fail
	// for the wrong reason.
	base := time.Now().UTC().Truncate(time.Second)

	for _, doc := range []bson.D{
		{{Key: "user_id", Value: "u-1"}, {Key: "created_at", Value: base.Add(-2 * time.Hour)}, {Key: "status", Value: "claimed"}},
		{{Key: "user_id", Value: "u-2"}, {Key: "created_at", Value: base.Add(-3 * time.Hour)}, {Key: "status", Value: "claimed"}},
		// A queued message with no user says nothing about anybody.
		{{Key: "created_at", Value: base}, {Key: "status", Value: "claimed"}},
	} {
		if _, err := srv.store.DB().Collection("inbound_queue").InsertOne(context.Background(), doc); err != nil {
			t.Fatalf("seed inbound: %v", err)
		}
	}
	if _, err := srv.store.DB().Collection("outbound_queue").InsertOne(context.Background(), bson.D{
		{Key: "user_id", Value: "u-1"}, {Key: "created_at", Value: base.Add(-time.Hour)}, {Key: "status", Value: "done"},
	}); err != nil {
		t.Fatalf("seed outbound: %v", err)
	}

	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/user-activity", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Activity map[string]string `json:"activity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Activity) != 2 {
		t.Fatalf("expected two users with activity, got %+v", body.Activity)
	}
	// The newer of the two queues wins.
	u1, err := time.Parse(time.RFC3339, body.Activity["u-1"])
	if err != nil {
		t.Fatalf("parse u-1 activity: %v", err)
	}
	if !u1.Equal(base.Add(-time.Hour)) {
		t.Fatalf("u-1 activity = %v, want the outbound one", u1)
	}
	u2, err := time.Parse(time.RFC3339, body.Activity["u-2"])
	if err != nil {
		t.Fatalf("parse u-2 activity: %v", err)
	}
	if !u2.Equal(base.Add(-3 * time.Hour)) {
		t.Fatalf("u-2 activity = %v", u2)
	}
}
