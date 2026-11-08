package store_test

// The session domain is the one this whole migration exists for: the write path
// changed from "replace a multi-MiB column every second" to "append the messages
// that are new". So these tests pin three things a driver mock could never show —
// that a delta really appends in place, that the counters stay reconcilable with
// the document (the plan's V-10), and that a delta which does not line up is
// refused rather than appended blindly.

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func messagesJSON(t *testing.T, raw ...string) string {
	t.Helper()
	out := make([]json.RawMessage, 0, len(raw))
	for _, r := range raw {
		out = append(out, json.RawMessage(r))
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	return string(encoded)
}

// readSessionDoc reads the stored document, which is the only way to see the
// messages array and the counters together.
func readSessionDoc(t *testing.T, s *store.Store, id string) struct {
	Messages      []any     `bson:"messages"`
	MessageCount  int       `bson:"message_count"`
	MessagesBytes int       `bson:"messages_bytes"`
	Title         string    `bson:"title"`
	Summary       string    `bson:"summary"`
	Status        string    `bson:"status"`
	TurnRunning   bool      `bson:"turn_running"`
	CreatedAt     time.Time `bson:"created_at"`
} {
	t.Helper()
	var doc struct {
		Messages      []any     `bson:"messages"`
		MessageCount  int       `bson:"message_count"`
		MessagesBytes int       `bson:"messages_bytes"`
		Title         string    `bson:"title"`
		Summary       string    `bson:"summary"`
		Status        string    `bson:"status"`
		TurnRunning   bool      `bson:"turn_running"`
		CreatedAt     time.Time `bson:"created_at"`
	}
	if err := s.DB().Collection("portal_sessions").
		FindOne(context.Background(), bson.D{{Key: "_id", Value: id}}).Decode(&doc); err != nil {
		t.Fatalf("read session %s: %v", id, err)
	}
	return doc
}

// seedSession inserts a session document directly, which is how a test gives one
// a status or an update time the API cannot set.
func seedSession(t *testing.T, s *store.Store, id, agentID, chatID, status string, updatedAt time.Time, messages ...string) {
	t.Helper()
	var decoded []any
	if err := json.Unmarshal([]byte(messagesJSON(t, messages...)), &decoded); err != nil {
		t.Fatalf("decode messages: %v", err)
	}
	if _, err := s.DB().Collection("portal_sessions").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id},
		{Key: "agent_id", Value: agentID},
		{Key: "account_id", Value: "user-1"},
		{Key: "channel", Value: "web"},
		{Key: "chat_id", Value: chatID},
		{Key: "messages", Value: decoded},
		{Key: "status", Value: status},
		{Key: "message_count", Value: len(decoded)},
		{Key: "messages_bytes", Value: len(messagesJSON(t, messages...))},
		{Key: "created_at", Value: updatedAt.Add(-time.Hour)},
		{Key: "updated_at", Value: updatedAt},
	}); err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
}

func TestSaveSession_DerivesTitleCountAndBytes(t *testing.T) {
	s := storetest.Connect(t)

	// The title heuristic runs over the stored form, so a CJK title is truncated
	// on rune boundaries and joined on whitespace.
	messages := messagesJSON(t,
		`{"role":"system","content":"sys"}`,
		`{"role":"user","content":"  Quarterly   Report  "}`,
		`{"role":"assistant","content":"done"}`,
	)
	if err := s.SaveSession(store.SessionRecord{
		ID: "web:chat-1", AgentID: "agent-1", AccountID: "user-1",
		Channel: "web", ChatID: "chat-1", Messages: messages,
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	doc := readSessionDoc(t, s, "web:chat-1")
	if doc.Title != "Quarterly Report" {
		t.Fatalf("title = %q, want %q", doc.Title, "Quarterly Report")
	}
	if doc.MessageCount != 3 {
		t.Fatalf("message_count = %d, want 3", doc.MessageCount)
	}
	if doc.MessagesBytes == 0 {
		t.Fatal("messages_bytes was not derived")
	}

	// The counters have to be reproducible by whoever re-measures the document:
	// that is what makes reconciliation (V-10) meaningful.
	canonical, err := json.Marshal(doc.Messages)
	if err != nil {
		t.Fatalf("marshal stored messages: %v", err)
	}
	if doc.MessagesBytes != len(canonical) || doc.MessageCount != len(doc.Messages) {
		t.Fatalf("counters do not reconcile: count=%d bytes=%d, document says %d/%d",
			doc.MessageCount, doc.MessagesBytes, len(doc.Messages), len(canonical))
	}
}

func TestSaveSession_TruncatesALongTitleOnRuneBoundaries(t *testing.T) {
	s := storetest.Connect(t)

	cjk := ""
	for i := 0; i < 70; i++ {
		cjk += "汉"
	}
	if err := s.SaveSession(store.SessionRecord{
		ID: "web:chat-1", AgentID: "agent-1", Channel: "web", ChatID: "chat-1",
		Messages: messagesJSON(t, `{"role":"user","content":"`+cjk+`"}`),
	}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	doc := readSessionDoc(t, s, "web:chat-1")
	runes := []rune(doc.Title)
	if len(runes) != 61 || runes[60] != '…' {
		t.Fatalf("title should be 60 runes plus an ellipsis, got %d: %q", len(runes), doc.Title)
	}
}

// TestSaveSession_ReplacesTheWholeConversation: the full path is a replacement,
// which is what keeps an older sender (or a resync) authoritative.
func TestSaveSession_ReplacesTheWholeConversation(t *testing.T) {
	s := storetest.Connect(t)

	three := messagesJSON(t, `{"role":"user","content":"a"}`, `{"role":"assistant","content":"b"}`, `{"role":"user","content":"c"}`)
	if err := s.SaveSession(store.SessionRecord{
		ID: "web:chat-1", AgentID: "agent-1", Channel: "web", ChatID: "chat-1", Messages: three,
	}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := s.SaveSession(store.SessionRecord{
		ID: "web:chat-1", AgentID: "agent-1", Channel: "web", ChatID: "chat-1",
		Messages: messagesJSON(t, `{"role":"user","content":"a"}`),
	}); err != nil {
		t.Fatalf("second save: %v", err)
	}

	doc := readSessionDoc(t, s, "web:chat-1")
	if doc.MessageCount != 1 || len(doc.Messages) != 1 {
		t.Fatalf("the second save merged instead of replacing: %+v", doc)
	}
}

// seedFullSession writes a session through the store, the way an agent's first
// snapshot does.
func seedFullSession(t *testing.T, s *store.Store, id string, messages ...string) {
	t.Helper()
	if err := s.SaveSession(store.SessionRecord{
		ID: id, AgentID: "agent-1", AccountID: "user-1", Channel: "web", ChatID: "chat-1",
		Messages: messagesJSON(t, messages...),
	}); err != nil {
		t.Fatalf("seed full session %s: %v", id, err)
	}
}

func TestSaveSession_AppendsADelta(t *testing.T) {
	s := storetest.Connect(t)
	seedFullSession(t, s, "web:chat-1",
		`{"role":"user","content":"one"}`,
		`{"role":"assistant","content":"two"}`)
	before := readSessionDoc(t, s, "web:chat-1")

	delta := messagesJSON(t, `{"role":"user","content":"three"}`, `{"role":"assistant","content":"four"}`)

	if err := s.SaveSession(store.SessionRecord{
		ID: "web:chat-1", AgentID: "agent-1", Channel: "web", ChatID: "chat-1",
		Messages:        delta,
		MessagesOmitted: true,
		FromIndex:       before.MessageCount,
		Summary:         "carried",
		TurnRunning:     true,
	}); err != nil {
		t.Fatalf("delta save: %v", err)
	}

	after := readSessionDoc(t, s, "web:chat-1")
	if len(after.Messages) != 4 || after.MessageCount != 4 {
		t.Fatalf("the delta did not append: %+v", after)
	}
	if after.MessageCount != len(after.Messages) {
		t.Fatalf("message_count does not match the array: %d vs %d", after.MessageCount, len(after.Messages))
	}
	// The stored form is what the sender's byte total was measured against, so
	// the two have to agree (V-10).
	stored, err := json.Marshal(after.Messages)
	if err != nil {
		t.Fatalf("marshal stored: %v", err)
	}
	if after.MessagesBytes != len(stored) {
		t.Fatalf("messages_bytes = %d, document measures %d", after.MessagesBytes, len(stored))
	}
	// The accumulation is exact by construction: the expected total is the byte
	// count the receiver had, plus the delta's own length minus its brackets.
	if want := before.MessagesBytes + len(delta) - 1; after.MessagesBytes != want {
		t.Fatalf("messages_bytes = %d, want %d", after.MessagesBytes, want)
	}
	if after.Summary != "carried" || !after.TurnRunning {
		t.Fatalf("the delta's small fields did not land: %+v", after)
	}
	// The messages themselves arrived in order — compared field by field, not byte
	// by byte: the write path encodes the sender's Go maps, and Go does not promise
	// a stable order for those, so the stored document's field order is a coin
	// flip. A byte comparison here failed roughly two runs in five.
	assertMessage(t, after.Messages[0], `{"role":"user","content":"one"}`)
	assertMessage(t, after.Messages[3], `{"role":"assistant","content":"four"}`)
}

// assertMessage compares one stored message with its JSON text, ignoring the order
// the fields happen to sit in.
func assertMessage(t *testing.T, stored any, want string) {
	t.Helper()
	encoded, err := json.Marshal(stored)
	if err != nil {
		t.Fatalf("marshal stored message: %v", err)
	}
	var got, expected any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode stored message %s: %v", encoded, err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("decode expected message %s: %v", want, err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("message = %s, want %s", encoded, want)
	}
}

// TestSaveSession_RefusesADeltaThatDoesNotLineUp is the guard that makes appending
// safe: if a snapshot failed to land, the stored conversation is not the prefix
// the delta claims, and the answer is a full resend — not a hole in the history.
func TestSaveSession_RefusesADeltaThatDoesNotLineUp(t *testing.T) {
	s := storetest.Connect(t)
	seedFullSession(t, s, "web:chat-1", `{"role":"user","content":"one"}`)

	err := s.SaveSession(store.SessionRecord{
		ID: "web:chat-1", AgentID: "agent-1", Channel: "web", ChatID: "chat-1",
		Messages:        messagesJSON(t, `{"role":"user","content":"two"}`),
		MessagesOmitted: true,
		FromIndex:       7, // the stored session holds one message, not seven
	})
	if !errors.Is(err, store.ErrSessionResync) {
		t.Fatalf("expected ErrSessionResync, got %v", err)
	}

	doc := readSessionDoc(t, s, "web:chat-1")
	if len(doc.Messages) != 1 || doc.MessageCount != 1 {
		t.Fatalf("a refused delta still changed the session: %+v", doc)
	}
}

// TestSaveSession_DeltaOnAMissingSessionAsksForAFullSend: an upsert would create
// a session whose first messages are missing, which no later delta could repair.
func TestSaveSession_DeltaOnAMissingSessionAsksForAFullSend(t *testing.T) {
	s := storetest.Connect(t)

	err := s.SaveSession(store.SessionRecord{
		ID: "web:ghost", AgentID: "agent-1", Channel: "web", ChatID: "ghost",
		Messages:        messagesJSON(t, `{"role":"user","content":"one"}`),
		MessagesOmitted: true,
		FromIndex:       0,
	})
	if !errors.Is(err, store.ErrSessionResync) {
		t.Fatalf("expected ErrSessionResync, got %v", err)
	}
	if got, err := s.GetSession("web:ghost"); err != nil || got != nil {
		t.Fatalf("a session was created from a delta: %+v (err %v)", got, err)
	}
}

// TestSaveSession_DeltaKeepsADerivedTitle: the bus derives a heuristic title from
// the conversation's *first* user message, which is not in a suffix — so an empty
// title on a delta means "nothing better", not "clear it".
func TestSaveSession_DeltaKeepsADerivedTitle(t *testing.T) {
	s := storetest.Connect(t)
	seedFullSession(t, s, "web:chat-1", `{"role":"user","content":"Quarterly Report"}`)
	before := readSessionDoc(t, s, "web:chat-1")
	if before.Title != "Quarterly Report" {
		t.Fatalf("the heuristic title was not derived: %q", before.Title)
	}

	delta := messagesJSON(t, `{"role":"assistant","content":"done"}`)
	if err := s.SaveSession(store.SessionRecord{
		ID: "web:chat-1", AgentID: "agent-1", Channel: "web", ChatID: "chat-1",
		Messages: delta, MessagesOmitted: true, FromIndex: before.MessageCount,
	}); err != nil {
		t.Fatalf("delta save: %v", err)
	}
	if doc := readSessionDoc(t, s, "web:chat-1"); doc.Title != "Quarterly Report" {
		t.Fatalf("the delta cleared the derived title: %q", doc.Title)
	}

	// A title the sender *does* have wins (the LLM-generated one).
	if err := s.SaveSession(store.SessionRecord{
		ID: "web:chat-1", AgentID: "agent-1", Channel: "web", ChatID: "chat-1",
		Messages: delta, MessagesOmitted: true, FromIndex: before.MessageCount + 1,
		Title: "LLM Title",
	}); err != nil {
		t.Fatalf("delta save: %v", err)
	}
	if doc := readSessionDoc(t, s, "web:chat-1"); doc.Title != "LLM Title" {
		t.Fatalf("the sender's title was not applied: %q", doc.Title)
	}
}

// TestSaveSession_CountersReconcile is the plan's V-10 in miniature: after a full
// snapshot and two deltas, a reader that re-measures the document must land on
// exactly the stored numbers.
func TestSaveSession_CountersReconcile(t *testing.T) {
	s := storetest.Connect(t)
	seedFullSession(t, s, "web:chat-1",
		`{"role":"user","content":"one"}`,
		`{"role":"assistant","content":"two"}`)

	for i := 0; i < 2; i++ {
		before := readSessionDoc(t, s, "web:chat-1")
		delta := messagesJSON(t, `{"role":"user","content":"more"}`)
		if err := s.SaveSession(store.SessionRecord{
			ID: "web:chat-1", AgentID: "agent-1", Channel: "web", ChatID: "chat-1",
			Messages: delta, MessagesOmitted: true, FromIndex: before.MessageCount,
		}); err != nil {
			t.Fatalf("delta save: %v", err)
		}
	}

	doc := readSessionDoc(t, s, "web:chat-1")
	measured, err := json.Marshal(doc.Messages)
	if err != nil {
		t.Fatalf("marshal stored: %v", err)
	}
	if doc.MessageCount != len(doc.Messages) || doc.MessagesBytes != len(measured) {
		t.Fatalf("counters drift from the document: %d/%d vs %d/%d",
			doc.MessageCount, doc.MessagesBytes, len(doc.Messages), len(measured))
	}
}

func TestGetSessionAndMeta(t *testing.T) {
	s := storetest.Connect(t)
	seedFullSession(t, s, "web:chat-1", `{"role":"user","content":"one"}`, `{"role":"assistant","content":"two"}`)

	rec, err := s.GetSession("web:chat-1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if rec == nil || rec.MessageCount != 2 {
		t.Fatalf("unexpected session: %+v", rec)
	}
	if rec.Messages == "" {
		t.Fatal("GetSession must carry the messages")
	}
	// The JSON a reader gets is an array of objects, not of key/value pairs: the
	// collection's DefaultDocumentM is what keeps the shape.
	var decoded []map[string]any
	if err := json.Unmarshal([]byte(rec.Messages), &decoded); err != nil {
		t.Fatalf("messages are not a JSON array: %v (%s)", err, rec.Messages)
	}
	if len(decoded) != 2 || decoded[0]["content"] != "one" {
		t.Fatalf("unexpected messages: %+v", decoded)
	}

	meta, err := s.GetSessionMeta("web:chat-1")
	if err != nil {
		t.Fatalf("GetSessionMeta: %v", err)
	}
	if meta == nil {
		t.Fatal("expected metadata")
	}
	if meta.Messages != "" {
		t.Fatalf("the metadata read carried the messages (%d bytes)", len(meta.Messages))
	}
	if meta.MessageCount != 2 || meta.MessagesBytes == 0 {
		t.Fatalf("metadata counters missing: %+v", meta)
	}

	// Missing rows read as nil, not as an error.
	if rec, err := s.GetSession("ghost"); err != nil || rec != nil {
		t.Fatalf("expected nil for a missing session, got %+v (err %v)", rec, err)
	}
	if rec, err := s.GetSessionMeta("ghost"); err != nil || rec != nil {
		t.Fatalf("expected nil metadata, got %+v (err %v)", rec, err)
	}
}

// TestListSessions_NewestFirstAndPaged pins the ordering the portal pages
// through: updated_at desc with the id as a tiebreak, and never the blob.
func TestListSessions_NewestFirstAndPaged(t *testing.T) {
	s := storetest.Connect(t)
	base := time.Now().UTC()
	seedSession(t, s, "web:chat-a", "agent-1", "chat-a", "active", base.Add(-2*time.Hour), `{"role":"user","content":"a"}`)
	seedSession(t, s, "web:chat-b", "agent-1", "chat-b", "active", base.Add(-time.Hour), `{"role":"user","content":"b"}`)
	seedSession(t, s, "web:chat-c", "agent-1", "chat-c", "active", base, `{"role":"user","content":"c"}`)
	// Another agent's session stays out of the list.
	seedSession(t, s, "web:other", "agent-2", "other", "active", base, `{"role":"user","content":"x"}`)

	sessions, err := s.ListSessions("agent-1", 2, 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(sessions) != 2 || sessions[0].ID != "web:chat-c" || sessions[1].ID != "web:chat-b" {
		t.Fatalf("expected the two newest first, got %+v", sessions)
	}
	if sessions[0].Messages != "" {
		t.Fatalf("the listing carried the messages (%d bytes)", len(sessions[0].Messages))
	}

	page2, err := s.ListSessions("agent-1", 2, 2)
	if err != nil {
		t.Fatalf("ListSessions page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].ID != "web:chat-a" {
		t.Fatalf("unexpected second page: %+v", page2)
	}

	total, err := s.CountSessions("agent-1")
	if err != nil {
		t.Fatalf("CountSessions: %v", err)
	}
	if total != 3 {
		t.Fatalf("count = %d, want 3", total)
	}
}

// TestListSessions_OrdersTiesByID: OFFSET paging over rows written in the same
// millisecond needs a deterministic order or a boundary can skip or repeat one.
func TestListSessions_OrdersTiesByID(t *testing.T) {
	s := storetest.Connect(t)
	same := time.Now().UTC()
	for _, id := range []string{"web:chat-b", "web:chat-a", "web:chat-c"} {
		seedSession(t, s, id, "agent-1", id, "active", same, `{"role":"user","content":"x"}`)
	}

	sessions, err := s.ListSessions("agent-1", 10, 0)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	want := []string{"web:chat-c", "web:chat-b", "web:chat-a"}
	for i, id := range want {
		if sessions[i].ID != id {
			t.Fatalf("expected %v, got %+v", want, sessions)
		}
	}
}

func TestArchiveSession(t *testing.T) {
	s := storetest.Connect(t)
	seedFullSession(t, s, "web:chat-1", `{"role":"user","content":"one"}`)

	if err := s.ArchiveSession("web:chat-1"); err != nil {
		t.Fatalf("ArchiveSession: %v", err)
	}
	rec, err := s.GetSessionMeta("web:chat-1")
	if err != nil {
		t.Fatalf("GetSessionMeta: %v", err)
	}
	if rec.Status != "archived" {
		t.Fatalf("status = %q, want archived", rec.Status)
	}
}

func TestBackfillSessionTitles(t *testing.T) {
	s := storetest.Connect(t)
	base := time.Now().UTC()
	// Two untitled sessions with messages, one untitled and empty (message_count
	// 0, so nothing to derive from), and one already titled.
	seedSession(t, s, "web:a", "agent-1", "a", "active", base, `{"role":"user","content":"First Title"}`)
	seedSession(t, s, "web:b", "agent-1", "b", "active", base, `{"role":"assistant","content":"no user msg"}`)
	seedSession(t, s, "web:empty", "agent-1", "empty", "active", base)
	seedFullSession(t, s, "web:titled", `{"role":"user","content":"already"}`)

	n, err := s.BackfillSessionTitles()
	if err != nil {
		t.Fatalf("BackfillSessionTitles: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 sessions backfilled, got %d", n)
	}

	if doc := readSessionDoc(t, s, "web:a"); doc.Title != "First Title" {
		t.Fatalf("title not backfilled: %q", doc.Title)
	}
	// A conversation with no user message gets the empty title, as it did before.
	if doc := readSessionDoc(t, s, "web:b"); doc.Title != "" {
		t.Fatalf("expected an empty title, got %q", doc.Title)
	}

	// The second run re-matches exactly the conversations it cannot title: their
	// title is still empty, because no user message was there to derive one from.
	// The SQL had the same property (`WHERE title = ''` matched them again), and
	// it is pinned rather than fixed — a marker field would be a schema change to
	// a one-shot backfill. What matters is that it does not touch the others.
	again, err := s.BackfillSessionTitles()
	if err != nil {
		t.Fatalf("second BackfillSessionTitles: %v", err)
	}
	if again != 1 {
		t.Fatalf("expected the one untitleable conversation, got %d", again)
	}
	if doc := readSessionDoc(t, s, "web:a"); doc.Title != "First Title" {
		t.Fatalf("a titled session was rewritten: %q", doc.Title)
	}
}

// TestListConversationAttention_NewestSegmentOnly replaces the SQL window
// function: a compaction leaves older segments of the same conversation behind,
// and their flags are history.
func TestListConversationAttention_NewestSegmentOnly(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedAgents(t, s, "user-1", "agent-1", "agent-2")

	base := time.Now().UTC()
	seedSession(t, s, "web:chat-1:v1", "agent-1", "chat-1", "active", base.Add(-time.Hour),
		`{"role":"user","content":"old segment"}`)
	seedSession(t, s, "web:chat-1:v2", "agent-1", "chat-1", "active", base,
		`{"role":"user","content":"new segment"}`)
	seedSession(t, s, "web:chat-2", "agent-2", "chat-2", "active", base,
		`{"role":"user","content":"other agent"}`)
	// An archived conversation and a non-web channel are not attention material.
	seedSession(t, s, "web:chat-3", "agent-1", "chat-3", "archived", base,
		`{"role":"user","content":"archived"}`)

	conversations, err := s.ListConversationAttention("user-1")
	if err != nil {
		t.Fatalf("ListConversationAttention: %v", err)
	}
	if len(conversations) != 2 {
		t.Fatalf("expected 2 conversations, got %+v", conversations)
	}
	byChat := map[string]store.ConversationAttention{}
	for _, c := range conversations {
		byChat[c.ChatID] = c
	}
	if got := byChat["chat-1"]; got.SessionID != "web:chat-1:v2" {
		t.Fatalf("expected the newest segment, got %+v", got)
	}
	if _, ok := byChat["chat-2"]; !ok {
		t.Fatalf("the second agent's conversation is missing: %+v", conversations)
	}
	if _, ok := byChat["chat-3"]; ok {
		t.Fatal("an archived conversation was reported")
	}

	// Another user's agents are not in scope.
	if other, err := s.ListConversationAttention("user-2"); err != nil || len(other) != 0 {
		t.Fatalf("another user saw conversations: %+v (err %v)", other, err)
	}
}

func TestMigrations_DeclareSessionIndexes(t *testing.T) {
	s := storetest.Connect(t)

	// The listing filters one agent and sorts (updated_at desc, _id desc) — all
	// three are in the key, so the sort is index-served and paging stays cheap.
	assertIndexKeys(t, s.DB().Collection("portal_sessions"), "agent_updated", [][2]any{
		{"agent_id", int32(1)}, {"updated_at", int32(-1)}, {"_id", int32(-1)},
	})
	assertIndexKeys(t, s.DB().Collection("portal_sessions"), "agent_channel_chat", [][2]any{
		{"agent_id", int32(1)}, {"channel", int32(1)}, {"chat_id", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("portal_sessions"), "agent_status", [][2]any{
		{"agent_id", int32(1)}, {"status", int32(1)},
	})
}

// TestListConversations_CrossAgentNewestFirst is the admin log's read: every
// agent's sessions, newest first, without the messages.
func TestListConversations_CrossAgentNewestFirst(t *testing.T) {
	s := storetest.Connect(t)
	base := time.Now().UTC()
	seedSession(t, s, "web:a", "agent-1", "a", "active", base.Add(-2*time.Hour), `{"role":"user","content":"a"}`)
	seedSession(t, s, "web:b", "agent-2", "b", "active", base.Add(-time.Hour), `{"role":"user","content":"b"}`)
	seedSession(t, s, "web:c", "agent-1", "c", "active", base, `{"role":"user","content":"c"}`)

	conversations, err := s.ListConversations(store.ConversationFilter{})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(conversations) != 3 {
		t.Fatalf("expected 3 conversations, got %d", len(conversations))
	}
	// Every agent's, newest first, and never the messages.
	if conversations[0].ID != "web:c" || conversations[1].ID != "web:b" || conversations[2].ID != "web:a" {
		t.Fatalf("unexpected order: %+v", conversations)
	}
	if conversations[0].AgentID != "agent-1" {
		t.Fatalf("unexpected agent: %+v", conversations[0])
	}
	if conversations[0].Messages != "" {
		t.Fatalf("the listing carried the messages (%d bytes)", len(conversations[0].Messages))
	}

	limited, err := s.ListConversations(store.ConversationFilter{Limit: 1})
	if err != nil {
		t.Fatalf("ListConversations: %v", err)
	}
	if len(limited) != 1 || limited[0].ID != "web:c" {
		t.Fatalf("limit not honoured: %+v", limited)
	}
}
