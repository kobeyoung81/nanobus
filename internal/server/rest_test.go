package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
	"gitee.com/kobeyoung81/nanobus/internal/portalagents"
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

	// The other domains answer over their internal APIs: the agent rows are the
	// portal's, the catalogue is the admin module's, the fleet and the queue are
	// the provisioner's. Seed these stubs, never their collections.
	agents := newAgentStub(t)
	s.agentSource = agents.client(t)
	agentStubs.Store(s, agents)
	catalogue := newCatalogueStub(t)
	s.catalogue = catalogue.client(t)
	catalogueStubs.Store(s, catalogue)
	fleet := newFleetStub(t)
	s.provisioner = fleet.client(t)
	fleetStubs.Store(s, fleet)
	return s
}

// The stubs a server under test was built with, keyed by server: seeding is a
// property of the instance, not of the package.
var (
	agentStubs     sync.Map
	catalogueStubs sync.Map
	fleetStubs     sync.Map
)

func agentStubFor(t *testing.T, s *Server) *agentStub {
	t.Helper()
	stub, ok := agentStubs.Load(s)
	if !ok {
		t.Fatal("this server has no agent stub")
	}
	return stub.(*agentStub)
}

func catalogueStubFor(t *testing.T, s *Server) *catalogueStub {
	t.Helper()
	stub, ok := catalogueStubs.Load(s)
	if !ok {
		t.Fatal("this server has no catalogue stub")
	}
	return stub.(*catalogueStub)
}

func fleetStubFor(t *testing.T, s *Server) *fleetStub {
	t.Helper()
	stub, ok := fleetStubs.Load(s)
	if !ok {
		t.Fatal("this server has no fleet stub")
	}
	return stub.(*fleetStub)
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

// The user list moved to nanoauth with the rest of the account surface (stage 1):
// the bus serves neither `GET /api/v1/users` nor `/api/v1/user-accounts` any more,
// so there is nothing here to assert. routes_test.go pins that they stay gone.

// seedAgent puts one agent row where the bus now reads it: the portal's stub.
//
// The rows are the portal's, so a test that needs an agent to exist seeds the
// answer the portal would give — not a collection the bus no longer opens.
func seedAgent(t *testing.T, s *Server, id, userID, status string) {
	t.Helper()
	seedAgentNamed(t, s, id, userID, status, id)
}

func seedAgentNamed(t *testing.T, s *Server, id, userID, status, displayName string) {
	t.Helper()
	agentStubFor(t, s).put(portalagents.Agent{
		ID: id, UserID: userID, TypeID: "build",
		DisplayName: displayName, Status: status,
		SessionAgents: []string{"build", "plan"}, DefaultAgent: "build",
		MaxIterations: 10,
	})
}
