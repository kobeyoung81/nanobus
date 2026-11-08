package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// The conversation log used to read agent_sessions out of MySQL. These two
// endpoints are what replaced that read, so what they must keep is the caller's
// shape: the portal's field names, and the messages passed through untouched.

func TestRestListConversations(t *testing.T) {
	srv := testServerMongo(t)
	seedSessionDoc(t, srv, "web:chat-1", "agent-1", "chat-1", "T", []string{"a", "b"},
		false, nil, timeNowUTC())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations?limit=10", nil)
	rec := httptest.NewRecorder()
	srv.restListConversations(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Conversations []struct {
			ID            string `json:"id"`
			AgentID       string `json:"agent_id"`
			ChatID        string `json:"chat_id"`
			Status        string `json:"status"`
			MessageCount  int    `json:"message_count"`
			MessagesBytes int64  `json:"messages_bytes"`
			UpdatedAt     string `json:"updated_at"`
		} `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Conversations) != 1 {
		t.Fatalf("expected one conversation, got %+v", body.Conversations)
	}
	got := body.Conversations[0]
	if got.ID != "web:chat-1" || got.AgentID != "agent-1" || got.ChatID != "chat-1" || got.Status != "active" {
		t.Fatalf("unexpected conversation: %+v", got)
	}
	if got.MessageCount != 2 || got.MessagesBytes == 0 || got.UpdatedAt == "" {
		t.Fatalf("the counters or timestamp are missing: %+v", got)
	}
	// The messages belong to the detail read, not the listing.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, present := raw["messages"]; present {
		t.Fatal("the listing carried messages")
	}
}

func TestRestGetConversation_PassesMessagesThrough(t *testing.T) {
	srv := testServerMongo(t)
	seedSessionDoc(t, srv, "web:chat-1", "agent-1", "chat-1", "T", []string{"a", "b"},
		false, nil, timeNowUTC())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/web:chat-1", nil)
	req.SetPathValue("sessionID", "web:chat-1")
	rec := httptest.NewRecorder()
	srv.restGetConversation(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		ID       string           `json:"id"`
		Summary  string           `json:"summary"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ID != "web:chat-1" {
		t.Fatalf("unexpected id: %q", body.ID)
	}
	// The messages arrive as the stored array — objects, not encoded strings.
	if len(body.Messages) != 2 || body.Messages[0]["role"] != "user" || body.Messages[1]["content"] != "b" {
		t.Fatalf("unexpected messages: %+v", body.Messages)
	}
}

func TestRestGetConversation_UnknownIs404(t *testing.T) {
	srv := testServerMongo(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations/ghost", nil)
	req.SetPathValue("sessionID", "ghost")
	rec := httptest.NewRecorder()
	srv.restGetConversation(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// timeNowUTC keeps the seeder call short.
func timeNowUTC() time.Time {
	return time.Now().UTC()
}

// setName writes the display name a document is known by. The log resolves names
// for its own page, so a test that filters by one needs the document to have it.
func setName(t *testing.T, srv *Server, collection, id, name string) {
	t.Helper()
	if _, err := srv.store.DB().Collection(collection).UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "display_name", Value: name}}}},
	); err != nil {
		t.Fatalf("set name on %s/%s: %v", collection, id, err)
	}
}

type conversationRow struct {
	ID          string `json:"id"`
	AgentID     string `json:"agent_id"`
	AgentName   string `json:"agent_name"`
	AccountID   string `json:"account_id"`
	AccountName string `json:"account_name"`
	Status      string `json:"status"`
}

func listConversations(t *testing.T, srv *Server, query string) []conversationRow {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/conversations?"+query, nil)
	rec := httptest.NewRecorder()
	srv.restListConversations(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Conversations []conversationRow `json:"conversations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Conversations
}

// TestRestListConversations_FiltersByOneConditionAtATime is the console's page:
// one control per condition. A name has to reach the row through the id it names,
// and the row has to come back carrying that name.
func TestRestListConversations_FiltersByOneConditionAtATime(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUser(t, srv.store, "user-1", "pro", 1)
	setName(t, srv, "auth_users", "user-1", "Bob")
	storetest.SeedAgents(t, srv.store, "user-1", "agent-1", "agent-2")
	setName(t, srv, "portal_agents", "agent-1", "Ada's agent")

	seedSessionDoc(t, srv, "web:chat-1", "agent-1", "chat-1", "T", []string{"a"}, false, nil, timeNowUTC())
	seedSessionDoc(t, srv, "web:chat-2", "agent-2", "chat-2", "T", []string{"b"}, false, nil, timeNowUTC())

	byName := listConversations(t, srv, "agent=ada")
	if len(byName) != 1 || byName[0].AgentID != "agent-1" {
		t.Fatalf("agent name filter: %+v", byName)
	}
	if byName[0].AgentName != "Ada's agent" || byName[0].AccountName != "Bob" {
		t.Fatalf("names were not attached to the row: %+v", byName[0])
	}

	byID := listConversations(t, srv, "agent=agent-2")
	if len(byID) != 1 || byID[0].AgentID != "agent-2" {
		t.Fatalf("agent id filter: %+v", byID)
	}

	byAccount := listConversations(t, srv, "account=bob")
	if len(byAccount) != 2 {
		t.Fatalf("account name filter: %+v", byAccount)
	}

	// Conditions are ANDed: an agent plus a status that no row has matches nothing.
	if got := listConversations(t, srv, "agent=ada&status=archived"); len(got) != 0 {
		t.Fatalf("combined conditions: %+v", got)
	}

	// The session id condition is a substring of the stored id.
	if got := listConversations(t, srv, "session=chat-1"); len(got) != 1 || got[0].ID != "web:chat-1" {
		t.Fatalf("session filter: %+v", got)
	}
}
