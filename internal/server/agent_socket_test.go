package server

// The agent socket's routing is what makes a push reach the right process: the
// replica that holds the connection pushes, and nothing routes between replicas.
// These tests pin that targeting without a database or a real network, and then
// one end-to-end case proves a queued message wakes the agent and stays
// unclaimed until the agent asks for it.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/stream"
	"gitee.com/kobeyoung81/nanoprotocol"
	"github.com/gorilla/websocket"
)

// recorder is a socket writer that keeps what was pushed.
type recorder struct {
	mu   sync.Mutex
	msgs []nanoprotocol.Message
}

func (r *recorder) write(v any) error {
	msg, ok := v.(nanoprotocol.Message)
	if !ok {
		if ptr, ok := v.(*nanoprotocol.Message); ok && ptr != nil {
			msg = *ptr
		}
	}
	r.mu.Lock()
	r.msgs = append(r.msgs, msg)
	r.mu.Unlock()
	return nil
}

func (r *recorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.msgs))
	for _, m := range r.msgs {
		out = append(out, m.Type)
	}
	return out
}

func (r *recorder) count(kind string) int {
	n := 0
	for _, t := range r.types() {
		if t == kind {
			n++
		}
	}
	return n
}

func fakeSocket(userID, agentID string, components ...string) (*agentSocket, *recorder) {
	rec := &recorder{}
	sock := &agentSocket{
		write:      rec.write,
		userID:     userID,
		agentID:    agentID,
		components: map[string]bool{},
		tokens:     agentSocketBurst,
		last:       time.Now(),
	}
	for _, c := range components {
		sock.components[c] = true
	}
	return sock, rec
}

// An inbound notice goes to the agent it names, to the sockets that cover every
// agent of that user, and to nobody else.
func TestAgentSocketRegistry_PushesInboundToTheAddressedSockets(t *testing.T) {
	reg := newAgentSocketRegistry()

	exact, exactRec := fakeSocket("u1", "a1", nanoprotocol.SocketComponentConfig)
	wildcard, wildcardRec := fakeSocket("u1", "", nanoprotocol.SocketComponentAgents)
	otherAgent, otherAgentRec := fakeSocket("u1", "a2", nanoprotocol.SocketComponentConfig)
	otherUser, otherUserRec := fakeSocket("u2", "a1", nanoprotocol.SocketComponentConfig)
	for _, s := range []*agentSocket{exact, wildcard, otherAgent, otherUser} {
		reg.register(s)
	}

	reg.pushInbound(stream.InboundNotice{UserID: "u1", AgentID: "a1", Status: "pending"})

	if exactRec.count(nanoprotocol.TypeInboundReady) != 1 {
		t.Fatalf("the addressed agent should be woken: %v", exactRec.types())
	}
	if wildcardRec.count(nanoprotocol.TypeInboundReady) != 1 {
		t.Fatalf("a socket covering every agent of the user should be woken: %v", wildcardRec.types())
	}
	if otherAgentRec.count(nanoprotocol.TypeInboundReady) != 0 {
		t.Fatalf("another agent of the same user must not be woken: %v", otherAgentRec.types())
	}
	if otherUserRec.count(nanoprotocol.TypeInboundReady) != 0 {
		t.Fatalf("another user must not be woken: %v", otherUserRec.types())
	}
}

// A message with no agent on it can be claimed by any agent of the user, so every
// socket of that user hears about it — that is the claim's own filter.
func TestAgentSocketRegistry_UnaddressedInboundWakesEveryAgentOfTheUser(t *testing.T) {
	reg := newAgentSocketRegistry()
	first, firstRec := fakeSocket("u1", "a1", nanoprotocol.SocketComponentConfig)
	second, secondRec := fakeSocket("u1", "a2", nanoprotocol.SocketComponentConfig)
	reg.register(first)
	reg.register(second)

	reg.pushInbound(stream.InboundNotice{UserID: "u1", AgentID: "", Status: "pending"})

	if firstRec.count(nanoprotocol.TypeInboundReady) != 1 || secondRec.count(nanoprotocol.TypeInboundReady) != 1 {
		t.Fatalf("every agent of the user should be woken: %v %v", firstRec.types(), secondRec.types())
	}
}

// A row that is already claimed is not news: waking the agent would only cost it
// a query.
func TestAgentSocketRegistry_SkipsClaimedInbound(t *testing.T) {
	reg := newAgentSocketRegistry()
	sock, rec := fakeSocket("u1", "a1", nanoprotocol.SocketComponentConfig)
	reg.register(sock)

	reg.pushInbound(stream.InboundNotice{UserID: "u1", AgentID: "a1", Status: "claimed"})

	if n := rec.count(nanoprotocol.TypeInboundReady); n != 0 {
		t.Fatalf("a claimed row must not be pushed, got %d", n)
	}
}

// Platform notices are the replacement for each process's two SSE streams, so
// the component subscription has to be honoured — an agent that only cares about
// configuration must not be woken for an agent record.
func TestAgentSocketRegistry_PlatformNoticesHonourTheSubscription(t *testing.T) {
	reg := newAgentSocketRegistry()
	configOnly, configRec := fakeSocket("u1", "a1", nanoprotocol.SocketComponentConfig)
	both, bothRec := fakeSocket("u1", "", nanoprotocol.SocketComponentConfig, nanoprotocol.SocketComponentAgents)
	reg.register(configOnly)
	reg.register(both)

	reg.pushPlatform(stream.PlatformEvent{Type: nanoprotocol.TypeAgentsChanged, Content: `{"user_id":"u1","action":"create"}`})
	if configRec.count(nanoprotocol.TypePlatformEvent) != 0 {
		t.Fatalf("a config-only socket must not get agents_changed: %v", configRec.types())
	}
	if bothRec.count(nanoprotocol.TypePlatformEvent) != 1 {
		t.Fatalf("the hypervisor socket should get agents_changed: %v", bothRec.types())
	}

	reg.pushPlatform(stream.PlatformEvent{Type: nanoprotocol.TypeConfigChanged, Content: `{"keys":["models"]}`})
	if configRec.count(nanoprotocol.TypePlatformEvent) != 1 {
		t.Fatalf("a config change should reach the agent: %v", configRec.types())
	}
}

// One socket may not turn into a way around the HTTP rate limiter.
func TestAgentSocket_RateLimitsOneConnection(t *testing.T) {
	sock, _ := fakeSocket("u1", "a1")
	now := time.Now()
	allowed := 0
	for i := 0; i < int(agentSocketBurst)+50; i++ {
		if sock.allow(now) {
			allowed++
		}
	}
	if allowed != int(agentSocketBurst) {
		t.Fatalf("expected the burst to bound one instant, got %d", allowed)
	}
	// A second of budget buys agentSocketRate more.
	allowed = 0
	for i := 0; i < 100; i++ {
		if sock.allow(now.Add(time.Second)) {
			allowed++
		}
	}
	if allowed != int(agentSocketRate) {
		t.Fatalf("expected the rate to refill one second's worth, got %d", allowed)
	}
}

// The socket is internal: without the shared token the handshake is refused.
func TestAgentSocket_RequiresTheInternalToken(t *testing.T) {
	s := wireServer(t, WithInternalToken("secret"))
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + nanoprotocol.AgentSocketPath
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		_ = conn.Close()
		t.Fatal("a socket without the token must not be accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %+v", resp)
	}
}

// End to end: a message posted to the queue wakes the registered agent, and the
// push carries no content — the message is still there to be claimed.
func TestAgentSocket_PushWakesTheAgentButLeavesTheMessageToClaim(t *testing.T) {
	s := testServerMongo(t)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s.startTailers()

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + nanoprotocol.AgentSocketPath
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	reg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeAgentSocketRegister, nanoprotocol.AgentSocketRegister{
		UserID: "user-1", AgentID: "agent-1",
	})
	if err := conn.WriteJSON(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	var ack nanoprotocol.Message
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("read ack: %v", err)
	}
	if ack.Type != nanoprotocol.TypeAgentSocketRegistered {
		t.Fatalf("expected the registration ack, got %s", ack.Type)
	}
	// The tailer starts at the current end; give it a moment so the message below
	// is an insert it sees rather than part of the starting position.
	time.Sleep(100 * time.Millisecond)

	if err := s.store.PostInbound(nanoprotocol.InboundMessage{
		UserID: "user-1", AgentID: "agent-1", SessionID: "session-1",
		Channel: "web", ChatID: "chat-1", SenderID: "sender-1", Content: "hello",
	}); err != nil {
		t.Fatalf("post inbound: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var push nanoprotocol.Message
	if err := conn.ReadJSON(&push); err != nil {
		t.Fatalf("expected an inbound_ready push: %v", err)
	}
	if push.Type != nanoprotocol.TypeInboundReady {
		t.Fatalf("expected inbound_ready, got %s", push.Type)
	}

	// The push is a wake-up only: the message is still pending until claimed.
	if got, err := s.store.ClaimInbound("user-1", 0, "agent-1"); err != nil || got == nil {
		t.Fatalf("the message must still be claimable: %v %v", got, err)
	}
}
