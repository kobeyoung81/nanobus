package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// The audit trail is written by the portal and read by the console. Both used to
// touch `audit_log` directly; the writes are here now, and the listing resolves
// the actor's name so the console does not have to join it back itself.

func TestRestRecordAudit(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUser(t, srv.store, "u-1", "pro", 1)
	if _, err := srv.store.DB().Collection("users").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "u-1"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "display_name", Value: "Ada"}}}},
	); err != nil {
		t.Fatalf("set display name: %v", err)
	}

	rec := accountRequest(t, srv, http.MethodPost, "/api/v1/audit", map[string]any{
		"actor_id":      "u-1",
		"actor_role":    "admin",
		"action":        "agent.delete",
		"resource_type": "agent",
		"resource_id":   "agent-1",
		"detail":        map[string]any{"why": "test"},
		"ip":            "10.0.0.1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	// The entry is readable, with the actor's name resolved.
	list := accountRequest(t, srv, http.MethodGet, "/api/v1/audit?limit=10", nil)
	var body struct {
		Entries []struct {
			ActorID      string         `json:"actor_id"`
			ActorName    string         `json:"actor_name"`
			Action       string         `json:"action"`
			ResourceType string         `json:"resource_type"`
			Detail       map[string]any `json:"detail"`
			IP           string         `json:"ip"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Entries) != 1 {
		t.Fatalf("expected one entry, got %+v", body.Entries)
	}
	got := body.Entries[0]
	if got.ActorID != "u-1" || got.Action != "agent.delete" || got.ResourceType != "agent" || got.IP != "10.0.0.1" {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got.ActorName != "Ada" {
		t.Fatalf("the actor's name was not resolved: %+v", got)
	}
	if got.Detail["why"] != "test" {
		t.Fatalf("detail lost: %+v", got.Detail)
	}
}

// TestRestRecordAudit_RequiresTheActionAndResource: a trail entry without those
// two would be unreadable, and the console cannot tell it apart from a mistake.
func TestRestRecordAudit_RequiresTheActionAndResource(t *testing.T) {
	srv := testServerMongo(t)

	rec := accountRequest(t, srv, http.MethodPost, "/api/v1/audit", map[string]any{"actor_id": "u-1"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	// An anonymous actor is recorded as the system, the way the bus's own trail
	// does it.
	rec = accountRequest(t, srv, http.MethodPost, "/api/v1/audit", map[string]any{
		"action": "config.update", "resource_type": "config",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

type auditRow struct {
	ActorID      string `json:"actor_id"`
	ActorName    string `json:"actor_name"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	ResourceName string `json:"resource_name"`
}

// TestRestListAudit_FiltersByOneConditionAtATime: the trail's page offers an
// action dropdown, an actor box, a resource-type dropdown and a resource box. A
// resource name has to reach the row through the id it names — and only for the
// type that name belongs to.
func TestRestListAudit_FiltersByOneConditionAtATime(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUser(t, srv.store, "u-1", "pro", 1)
	storetest.SeedUser(t, srv.store, "u-2", "pro", 2)
	storetest.SeedAgents(t, srv.store, "u-2", "agent-1")
	setName(t, srv, "users", "u-1", "Ada")
	setName(t, srv, "users", "u-2", "Bob")
	setName(t, srv, "agents", "agent-1", "ops-bot")

	for _, entry := range []map[string]any{
		{"actor_id": "u-1", "actor_role": "admin", "action": "agent.delete", "resource_type": "agent", "resource_id": "agent-1"},
		{"actor_id": "u-2", "actor_role": "operator", "action": "user.update", "resource_type": "user", "resource_id": "u-1"},
	} {
		if rec := accountRequest(t, srv, http.MethodPost, "/api/v1/audit", entry); rec.Code != http.StatusCreated {
			t.Fatalf("seed audit entry: %d %s", rec.Code, rec.Body.String())
		}
	}

	list := func(query string) []auditRow {
		t.Helper()
		rec := accountRequest(t, srv, http.MethodGet, "/api/v1/audit?"+query, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("list audit: %d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Entries []auditRow `json:"entries"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.Entries
	}

	if got := list("actor=ada"); len(got) != 1 || got[0].ActorID != "u-1" || got[0].ActorName != "Ada" {
		t.Fatalf("actor name filter: %+v", got)
	}

	byResource := list("resource=ops")
	if len(byResource) != 1 || byResource[0].ResourceID != "agent-1" {
		t.Fatalf("resource name filter: %+v", byResource)
	}
	if byResource[0].ResourceName != "ops-bot" {
		t.Fatalf("the resource's name was not resolved: %+v", byResource[0])
	}

	byUser := list("resource=ada&resource_type=user")
	if len(byUser) != 1 || byUser[0].ResourceID != "u-1" || byUser[0].ResourceName != "Ada" {
		t.Fatalf("resource name scoped by type: %+v", byUser)
	}

	if got := list("action=agent.delete"); len(got) != 1 || got[0].Action != "agent.delete" {
		t.Fatalf("action filter: %+v", got)
	}

	// A resource name that belongs to an agent must not match a user row of the
	// same name fragment, because the branches are scoped by type.
	if got := list("resource=ops&resource_type=user"); len(got) != 0 {
		t.Fatalf("type-scoped name branch leaked: %+v", got)
	}
}
