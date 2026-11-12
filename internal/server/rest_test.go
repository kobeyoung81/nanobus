package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
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
	st := storetest.Connect(t)
	s := NewServer(st)
	// The account surface belongs to nanoauth now, so a server under test needs a
	// stand-in for it: an in-memory backend for the rows, and a stub for the
	// directory roster the user list asks about.
	fake := storetest.NewFakeAccounts()
	st.SetAccountBackend(fake)
	s.auth = emptyAuthStub(t)
	accountFakes.Store(s, fake)
	return s
}

// emptyAuthStub answers the two link reads with nothing linked. It is deliberately
// minimal: a test whose subject is a link uses the client's own tests.
// accountFakes maps a server under test to its in-memory account backend, so a test
// can assert on accounts without reaching into the store's wiring.
var accountFakes sync.Map

// fakeAccountsFor returns the backend a server under test was built with.
func fakeAccountsFor(t *testing.T, s *Server) *storetest.FakeAccounts {
	t.Helper()
	fake, ok := accountFakes.Load(s)
	if !ok {
		t.Fatal("this server has no account backend")
	}
	return fake.(*storetest.FakeAccounts)
}

func emptyAuthStub(t *testing.T) *authclient.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"links":[]}`))
	}))
	t.Cleanup(srv.Close)
	return authclient.New(srv.URL, "test-token")
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
