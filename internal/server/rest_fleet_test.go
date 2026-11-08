package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// The fleet view and the runtime snapshot. Both were SELECTs against MySQL; what
// they must keep is the ordering (newest registration first) and the fields the
// console prints — including the runner a container landed on, which the
// agent-facing shape does not carry.

func seedRunnerDoc(t *testing.T, s *Server, id, hostname, status string, registeredAt time.Time) {
	t.Helper()
	if _, err := s.store.DB().Collection("runners").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id},
		{Key: "hostname", Value: hostname},
		{Key: "docker_host", Value: "local"},
		{Key: "status", Value: status},
		{Key: "last_heartbeat", Value: registeredAt.Add(time.Minute)},
		{Key: "registered_at", Value: registeredAt},
	}); err != nil {
		t.Fatalf("seed runner %s: %v", id, err)
	}
}

func seedContainerDoc(t *testing.T, s *Server, userID, level, status, runnerID string, updatedAt time.Time) {
	t.Helper()
	doc := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "level", Value: level},
		{Key: "status", Value: status},
		{Key: "created_at", Value: updatedAt},
		{Key: "updated_at", Value: updatedAt},
	}
	if runnerID != "" {
		doc = append(doc, bson.E{Key: "runner_id", Value: runnerID})
	}
	if _, err := s.store.DB().Collection("container_requests").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed container for %s: %v", userID, err)
	}
}

func TestRestListRunners(t *testing.T) {
	srv := testServerMongo(t)
	base := time.Now().UTC()
	seedRunnerDoc(t, srv, "runner-old", "host-old", "offline", base.Add(-time.Hour))
	seedRunnerDoc(t, srv, "runner-new", "host-new", "online", base)

	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/runners", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Runners []struct {
			ID            string `json:"id"`
			Hostname      string `json:"hostname"`
			DockerHost    string `json:"docker_host"`
			Status        string `json:"status"`
			LastHeartbeat string `json:"last_heartbeat"`
			RegisteredAt  string `json:"registered_at"`
		} `json:"runners"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Runners) != 2 {
		t.Fatalf("expected two runners, got %+v", body.Runners)
	}
	// Most recently registered first.
	if body.Runners[0].ID != "runner-new" || body.Runners[1].ID != "runner-old" {
		t.Fatalf("unexpected order: %+v", body.Runners)
	}
	got := body.Runners[0]
	if got.Hostname != "host-new" || got.DockerHost != "local" || got.Status != "online" {
		t.Fatalf("unexpected runner: %+v", got)
	}
	if got.LastHeartbeat == "" || got.RegisteredAt == "" {
		t.Fatalf("timestamps missing: %+v", got)
	}

	counts := accountRequest(t, srv, http.MethodGet, "/api/v1/runners/counts", nil)
	var countBody struct {
		Online int `json:"online"`
	}
	if err := json.Unmarshal(counts.Body.Bytes(), &countBody); err != nil {
		t.Fatalf("decode counts: %v", err)
	}
	if countBody.Online != 1 {
		t.Fatalf("online = %d, want 1", countBody.Online)
	}
}

func TestRestListContainerRequests(t *testing.T) {
	srv := testServerMongo(t)
	base := time.Now().UTC()
	seedContainerDoc(t, srv, "u-1", "pro", "running", "runner-1", base.Add(-time.Hour))
	seedContainerDoc(t, srv, "u-1", "pro", "pending", "", base)
	seedContainerDoc(t, srv, "u-2", "free", "running", "runner-1", base)

	// One user's containers, newest first, with the runner they landed on.
	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/container-requests?user_id=u-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Containers []struct {
			ID        string `json:"id"`
			UserID    string `json:"user_id"`
			Level     string `json:"level"`
			Status    string `json:"status"`
			RunnerID  string `json:"runner_id"`
			UpdatedAt string `json:"updated_at"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Containers) != 2 {
		t.Fatalf("expected u-1's two containers, got %+v", body.Containers)
	}
	if body.Containers[0].Status != "pending" || body.Containers[1].Status != "running" {
		t.Fatalf("unexpected order: %+v", body.Containers)
	}
	if body.Containers[1].RunnerID != "runner-1" || body.Containers[1].Level != "pro" {
		t.Fatalf("unexpected container: %+v", body.Containers[1])
	}
	if body.Containers[0].UpdatedAt == "" || body.Containers[0].ID == "" {
		t.Fatalf("id or timestamp missing: %+v", body.Containers[0])
	}

	// Without a user it is the runtime snapshot's view: everybody's.
	all := accountRequest(t, srv, http.MethodGet, "/api/v1/container-requests", nil)
	if err := json.Unmarshal(all.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Containers) != 3 {
		t.Fatalf("expected all three containers, got %+v", body.Containers)
	}

	counts := accountRequest(t, srv, http.MethodGet, "/api/v1/container-requests/counts", nil)
	var countBody struct {
		ByStatus map[string]int `json:"by_status"`
	}
	if err := json.Unmarshal(counts.Body.Bytes(), &countBody); err != nil {
		t.Fatalf("decode counts: %v", err)
	}
	// The console sums the buckets it cares about: two running, one pending.
	if countBody.ByStatus["running"] != 2 || countBody.ByStatus["pending"] != 1 {
		t.Fatalf("unexpected buckets: %+v", countBody.ByStatus)
	}
}
