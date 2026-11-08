package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// wireServer builds a Server for tests whose subject is the HTTP wiring itself: a
// guard, a route, a header. The store is deliberately absent — these tests never
// consult it, and demanding a database for them would skip real coverage (the
// internal-key guard, for one) on a machine that has none. Anything that reads or
// writes state belongs in testServerMongo instead.
func wireServer(t *testing.T, opts ...ServerOption) *Server {
	t.Helper()
	return NewServer(nil, opts...)
}

// testServerMongo builds a Server around a real, freshly migrated MongoDB.
//
// It is the way to build a server whose endpoints read state: every domain lives
// in the store now, and a mock would have nothing to answer with. A test whose
// subject is the HTTP wiring itself (a guard, a route) uses wireServer instead,
// so it does not need a database at all.
func testServerMongo(t *testing.T) *Server {
	t.Helper()
	return NewServer(storetest.Connect(t))
}

func TestRestPostInbound_Success(t *testing.T) {
	// The queue lives in MongoDB now, so this asserts the effect rather than the
	// statement: the message is claimed back through the same store the server
	// uses.
	s := testServerMongo(t)

	body := `{"user_id":"user-1","session_id":"session-1","channel":"web","chat_id":"chat-1","sender_id":"sender-1","message_id":"msg-1","content":"Hello"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	msg, err := s.store.ClaimInbound("user-1", 0, "")
	if err != nil {
		t.Fatalf("ClaimInbound: %v", err)
	}
	if msg == nil {
		t.Fatal("the accepted message is not in the queue")
	}
	if msg.MessageID != "msg-1" || msg.Content != "Hello" || msg.SessionID != "session-1" {
		t.Fatalf("queued message does not match the request: %+v", msg)
	}
}

func TestRestPostInbound_BadJSON(t *testing.T) {
	s := wireServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/inbound", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestRestResolveUser_Existing(t *testing.T) {
	// The identity and its owner are one document now, so this needs the real
	// store rather than a mocked lookup.
	s := testServerMongo(t)
	storetest.SeedUserChannel(t, s.store, "user-1", "pro", "telegram", "tg-123", 1)

	body := `{"channel":"telegram","sender_id":"tg-123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/resolve", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRestCreateUser_Success(t *testing.T) {
	// The registry is a collection now, so this asserts the effect: the user is
	// read back through the store.
	s := testServerMongo(t)

	body := `{"id":"new-user-123","display_name":"New User"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	users, err := s.store.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0] != "new-user-123" {
		t.Fatalf("the user was not registered: %v", users)
	}
}

func TestRestListUsers(t *testing.T) {
	s := testServerMongo(t)
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)
	storetest.SeedUser(t, s.store, "user-2", "free", 2)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRestListUsers_Empty(t *testing.T) {
	s := testServerMongo(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rec := httptest.NewRecorder()

	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

// seedAgent writes an agent straight into the store's MongoDB.
//
// The agent domain moved there, so a test that needs an agent to exist must put
// one there: mocking SQL rows for it now mocks a query nobody runs.
func seedAgent(t *testing.T, s *Server, id, userID, status string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := s.store.DB().Collection("portal_agents").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id},
		{Key: "user_id", Value: userID},
		{Key: "type_id", Value: "build"},
		{Key: "display_name", Value: id},
		{Key: "max_iterations", Value: 10},
		{Key: "max_concurrent_messages", Value: 4},
		{Key: "status", Value: status},
		{Key: "created_at", Value: now},
		{Key: "updated_at", Value: now},
	}); err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
}

// seedAgentNamed writes an agent with a specific display name, for the tests that
// exercise the name-uniqueness rule.
func seedAgentNamed(t *testing.T, s *Server, id, userID, status, displayName string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := s.store.DB().Collection("portal_agents").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id},
		{Key: "user_id", Value: userID},
		{Key: "type_id", Value: "build"},
		{Key: "display_name", Value: displayName},
		{Key: "status", Value: status},
		{Key: "max_iterations", Value: 10},
		{Key: "max_concurrent_messages", Value: 4},
		{Key: "created_at", Value: now},
		{Key: "updated_at", Value: now},
	}); err != nil {
		t.Fatalf("seed agent %s: %v", id, err)
	}
}
