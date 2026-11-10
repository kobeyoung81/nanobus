package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// seedSessionDoc writes a session document. The endpoint's answer is computed
// from it, so these tests assert the response rather than a canned row — and the
// ETag they revalidate with is the one the store derives, not a literal.

func seedSessionDoc(t *testing.T, srv *Server, id, agentID, chatID, title string, messages []string, turnRunning bool, turnUpdatedAt *time.Time, updatedAt time.Time) {
	t.Helper()
	decoded := make([]any, 0, len(messages))
	for _, m := range messages {
		decoded = append(decoded, bson.D{{Key: "role", Value: "user"}, {Key: "content", Value: m}})
	}
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: "agent_id", Value: agentID},
		{Key: "account_id", Value: "user-1"},
		{Key: "channel", Value: "web"},
		{Key: "chat_id", Value: chatID},
		{Key: "messages", Value: decoded},
		{Key: "status", Value: "active"},
		{Key: "message_count", Value: len(decoded)},
		{Key: "messages_bytes", Value: 64},
		{Key: "turn_running", Value: turnRunning},
		{Key: "created_at", Value: updatedAt.Add(-time.Hour)},
		{Key: "updated_at", Value: updatedAt},
	}
	if title != "" {
		doc = append(doc, bson.E{Key: "title", Value: title})
	}
	if turnUpdatedAt != nil {
		doc = append(doc, bson.E{Key: "turn_updated_at", Value: *turnUpdatedAt})
	}
	if _, err := srv.store.DB().Collection("portal_sessions").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

// etagOf builds the validator the endpoint would answer with, from the stored
// session — the same three fields sessionETag fingerprints.
func etagOf(t *testing.T, srv *Server, id string) string {
	t.Helper()
	rec, err := srv.store.GetSessionMeta(id)
	if err != nil {
		t.Fatalf("GetSessionMeta: %v", err)
	}
	if rec == nil {
		t.Fatalf("session %s was not seeded", id)
	}
	return fmt.Sprintf("W/\"%d-%d-%d\"", rec.MessageCount, rec.MessagesBytes, rec.UpdatedAt.Unix())
}

// TestRestGetSession_MetaOnlySkipsMessages pins the gateway's resume path: the
// metadata read returns the ownership fields and the title without the messages.
func TestRestGetSession_MetaOnlySkipsMessages(t *testing.T) {
	srv := testServerMongo(t)
	seedSessionDoc(t, srv, "web:chat-1", "agent-1", "chat-1", "T",
		[]string{"a", "b"}, false, nil, time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC))

	req := httptest.NewRequest("GET", "/api/v1/sessions/web:chat-1?meta=1", nil)
	req.SetPathValue("sessionID", "web:chat-1")
	rec := httptest.NewRecorder()

	srv.restGetSession(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["messages"]; ok {
		t.Fatal("meta-only response must not carry messages")
	}
	if body["chat_id"] != "chat-1" || body["title"] != "T" || body["message_count"] != float64(2) {
		t.Fatalf("unexpected meta-only body: %v", body)
	}
}

// TestRestGetSession_NotModifiedSkipsBlob pins the ETag path: a matching
// validator is answered 304 with no body. The blob is not read on the way — the
// metadata read projects it away, which is where that guarantee lives now.
func TestRestGetSession_NotModifiedSkipsBlob(t *testing.T) {
	srv := testServerMongo(t)
	seedSessionDoc(t, srv, "web:chat-1", "agent-1", "chat-1", "T",
		[]string{"a", "b"}, false, nil, time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC))
	etag := etagOf(t, srv, "web:chat-1")

	req := httptest.NewRequest("GET", "/api/v1/sessions/web:chat-1?limit=30", nil)
	req.SetPathValue("sessionID", "web:chat-1")
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()

	srv.restGetSession(rec, req)

	if rec.Code != 304 {
		t.Fatalf("status = %d, want 304 (%s)", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 must not carry a body, got %q", rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != etag {
		t.Fatalf("ETag = %q, want %q", got, etag)
	}
}

// TestRestGetSession_ChangedReturnsPage checks the normal path still works and
// carries the title and the byte size.
func TestRestGetSession_ChangedReturnsPage(t *testing.T) {
	srv := testServerMongo(t)
	seedSessionDoc(t, srv, "web:chat-1", "agent-1", "chat-1", "T",
		[]string{"a", "b"}, false, nil, time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC))

	req := httptest.NewRequest("GET", "/api/v1/sessions/web:chat-1?limit=30", nil)
	req.SetPathValue("sessionID", "web:chat-1")
	req.Header.Set("If-None-Match", `W/"stale"`)
	rec := httptest.NewRecorder()

	srv.restGetSession(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["total"] != float64(2) || body["has_more"] != false {
		t.Fatalf("unexpected page body: %v", body)
	}
	if _, ok := body["messages"]; !ok {
		t.Fatal("expected a messages page")
	}
	if body["title"] != "T" || body["messages_bytes"] != float64(64) {
		t.Fatalf("page must carry title/bytes: %v", body)
	}
}

func TestRestGetSession_UnknownIs404(t *testing.T) {
	srv := testServerMongo(t)

	req := httptest.NewRequest("GET", "/api/v1/sessions/ghost?meta=1", nil)
	req.SetPathValue("sessionID", "ghost")
	rec := httptest.NewRecorder()
	srv.restGetSession(rec, req)

	if rec.Code != 404 {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestRestGetSession_MarksTheResponseRevalidatable(t *testing.T) {
	srv := testServerMongo(t)
	seedSessionDoc(t, srv, "web:chat-1", "agent-1", "chat-1", "T",
		[]string{"a", "b"}, false, nil, time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC))

	req := httptest.NewRequest("GET", "/api/v1/sessions/web:chat-1?limit=30", nil)
	req.SetPathValue("sessionID", "web:chat-1")
	req.Header.Set("If-None-Match", etagOf(t, srv, "web:chat-1"))
	rec := httptest.NewRecorder()

	srv.restGetSession(rec, req)

	if rec.Code != 304 {
		t.Fatalf("status = %d, want 304 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q, want %q", got, "private, no-cache")
	}
	if got := rec.Header().Get("ETag"); got == "" {
		t.Fatal("expected an ETag to revalidate with")
	}
}

// TestRestGetSession_MetaPathIsRevalidatableToo covers the metadata read the
// gateway uses: same caching contract, no body involved.
func TestRestGetSession_MetaPathIsRevalidatableToo(t *testing.T) {
	srv := testServerMongo(t)
	seedSessionDoc(t, srv, "web:chat-1", "agent-1", "chat-1", "T",
		[]string{"a", "b"}, false, nil, time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC))

	req := httptest.NewRequest("GET", "/api/v1/sessions/web:chat-1?meta=1", nil)
	req.SetPathValue("sessionID", "web:chat-1")
	rec := httptest.NewRecorder()

	srv.restGetSession(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, no-cache" {
		t.Fatalf("Cache-Control = %q, want %q", got, "private, no-cache")
	}
}

func TestRestListSessions_StaleFlagReadsNotRunning(t *testing.T) {
	srv := testServerMongo(t)
	now := time.Now().UTC()
	stale := now.Add(-45 * time.Second)
	fresh := now.Add(-2 * time.Second)
	seedSessionDoc(t, srv, "web:stale", "agent-1", "stale", "S", []string{"a"}, true, &stale, now)
	seedSessionDoc(t, srv, "web:fresh", "agent-1", "fresh", "F", []string{"a"}, true, &fresh, now)

	req := httptest.NewRequest("GET", "/api/v1/sessions?agent_id=agent-1", nil)
	rec := httptest.NewRecorder()
	srv.restListSessions(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Sessions []struct {
			ID          string `json:"id"`
			TurnRunning bool   `json:"turn_running"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(body.Sessions))
	}
	for _, session := range body.Sessions {
		want := session.ID == "web:fresh"
		if session.TurnRunning != want {
			t.Fatalf("%s turn_running = %v, want %v", session.ID, session.TurnRunning, want)
		}
	}
}

func TestMatchesETag(t *testing.T) {
	const etag = `W/"12-4096-100"`
	for _, tc := range []struct {
		header string
		want   bool
	}{
		{"", false},
		{`W/"12-4096-100"`, true},
		{`"12-4096-100"`, true}, // strong form of the same value
		{`W/"other", W/"12-4096-100"`, true},
		{"*", true},
		{`W/"12-4096-101"`, false},
	} {
		if got := matchesETag(tc.header, etag); got != tc.want {
			t.Fatalf("matchesETag(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

// TestRestGetSession_MarksTheResponseRevalidatable pins the browser-side half of
// the refresh path: the response may be stored, but must always be revalidated,
// which is what lets the ETag turn a reload into a 304 without a body.

// TestTurnRunningStaleWindow pins the age at which a persisted turn_running flag
// stops being believed. A crashed agent leaves the flag set; the portal shows its
// running indicator until a read says otherwise, so this window is what bounds
// how long a stale indicator can survive. It must stay comfortably above the
// agent's write cadence (a snapshot every 2s, flushed to MySQL every second).
func TestTurnRunningStaleWindow(t *testing.T) {
	// Floor: the window must stay comfortably above the agent's write cadence
	// (a session snapshot every 2s, flushed to MySQL every second by default), or
	// a healthy turn could be reported as finished.
	if turnRunningStaleAfter < 10*time.Second {
		t.Fatalf("turnRunningStaleAfter = %v, want at least 10s", turnRunningStaleAfter)
	}
	now := time.Now()
	tests := []struct {
		name      string
		running   bool
		updatedAt time.Time
		want      bool
	}{
		{name: "fresh flag", running: true, updatedAt: now.Add(-5 * time.Second), want: true},
		{name: "within the window", running: true, updatedAt: now.Add(-turnRunningStaleAfter / 2), want: true},
		{name: "just past the window", running: true, updatedAt: now.Add(-turnRunningStaleAfter - time.Second), want: false},
		{name: "crashed agent", running: true, updatedAt: now.Add(-45 * time.Second), want: false},
		{name: "not running", running: false, updatedAt: now, want: false},
		{name: "no timestamp", running: true, updatedAt: time.Time{}, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := turnRunning(tc.running, tc.updatedAt); got != tc.want {
				t.Fatalf("turnRunning(%v, %v) = %v, want %v", tc.running, tc.updatedAt, got, tc.want)
			}
		})
	}
}

// TestRestListSessions_StaleFlagReadsNotRunning pins that an agent's session
// list applies the same freshness window as a single-session read. A crashed
// agent leaves turn_running set, and the sidebar's running dot must not outlive
// the window — the list used to return the stored flag verbatim.
