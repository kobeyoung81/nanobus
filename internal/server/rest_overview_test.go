package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// The overview's remaining numbers. Each was a SELECT against MySQL, and each has a
// shape the console prints directly — which is what these tests pin.

func TestRestCountQueues(t *testing.T) {
	srv := testServerMongo(t)

	for _, doc := range []bson.D{
		{{Key: "user_id", Value: "u-1"}, {Key: "status", Value: "pending"}},
		{{Key: "user_id", Value: "u-1"}, {Key: "status", Value: "pending"}},
		// Anything not pending is not a backlog.
		{{Key: "user_id", Value: "u-1"}, {Key: "status", Value: "claimed"}},
	} {
		if _, err := srv.store.DB().Collection("bus_inbound_queue").InsertOne(context.Background(), doc); err != nil {
			t.Fatalf("seed inbound: %v", err)
		}
	}
	if _, err := srv.store.DB().Collection("bus_outbound_queue").InsertOne(context.Background(),
		bson.D{{Key: "user_id", Value: "u-1"}, {Key: "status", Value: "pending"}}); err != nil {
		t.Fatalf("seed outbound: %v", err)
	}

	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/queues/counts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Inbound  int `json:"inbound_pending"`
		Outbound int `json:"outbound_pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Inbound != 2 || body.Outbound != 1 {
		t.Fatalf("depths = %+v, want 2/1", body)
	}
}

func TestRestCountWeChatKF(t *testing.T) {
	srv := testServerMongo(t)

	if err := srv.store.UpdateWeChatKFCursor("kf_1", "c1"); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	if err := srv.store.UpdateWeChatKFCursor("kf_2", "c2"); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/wechat-kf/counts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Accounts int `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Accounts != 2 {
		t.Fatalf("accounts = %d, want 2", body.Accounts)
	}
}

func TestRestConfigCountsAndUpdatedAt(t *testing.T) {
	srv := testServerMongo(t)

	for _, item := range []store.ConfigItem{
		{Key: "channels.web.enabled", Value: "true"},
		{Key: "channels.telegram.enabled", Value: "false"},
		{Key: "channels.telegram.token", Value: "secret"},
		{Key: "port", Value: "8080"},
	} {
		if err := srv.store.SetConfigItem("gateway", item.Key, item.Value); err != nil {
			t.Fatalf("seed config: %v", err)
		}
	}

	// How many channels are enabled: a pattern over keys plus a value filter.
	rec := accountRequest(t, srv, http.MethodGet,
		"/api/v1/config-counts/gateway?key_prefix=channels.&key_suffix=.enabled&value=true", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var countBody struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &countBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if countBody.Count != 1 {
		t.Fatalf("count = %d, want 1 (only channels.web.enabled is true)", countBody.Count)
	}

	// An unknown component has no write time, and that is not an error.
	missing := accountRequest(t, srv, http.MethodGet, "/api/v1/config-updated-at/nobody", nil)
	if missing.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", missing.Code)
	}
	var updatedBody struct {
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal(missing.Body.Bytes(), &updatedBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updatedBody.UpdatedAt != "" {
		t.Fatalf("expected an empty timestamp, got %q", updatedBody.UpdatedAt)
	}

	// A component with configuration reports when it last changed.
	known := accountRequest(t, srv, http.MethodGet, "/api/v1/config-updated-at/gateway", nil)
	if err := json.Unmarshal(known.Body.Bytes(), &updatedBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updatedBody.UpdatedAt == "" {
		t.Fatal("expected a timestamp for a configured component")
	}
}
