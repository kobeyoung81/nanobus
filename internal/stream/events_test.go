package stream

// The event log is the replay source: ordering within a conversation, cursor
// semantics and trimming are all server-side behaviours (ObjectId order, index
// range scans), so these run against a real MongoDB and skip when none is
// configured.

import (
	"context"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st := storetest.Connect(t)
	return NewStore(st.DB())
}

func TestAppendAndSince(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	first, err := s.Append(ctx, Event{Channel: "web", ChatID: "c1", Type: "chunk", Content: "a"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	second, err := s.Append(ctx, Event{Channel: "web", ChatID: "c1", Type: "chunk", Content: "b"})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := s.Append(ctx, Event{Channel: "web", ChatID: "c2", Type: "chunk", Content: "other"}); err != nil {
		t.Fatalf("append other: %v", err)
	}

	events, err := s.Since(ctx, "web", "c1", "", 100)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(events) != 2 || events[0].Content != "a" || events[1].Content != "b" {
		t.Fatalf("events = %+v", events)
	}
	if events[0].ID.Hex() != first || events[1].ID.Hex() != second {
		t.Fatalf("ids do not match the appends: %s %s", first, second)
	}

	// Cursor semantics: strictly after the given id, and never another
	// conversation's events.
	after, err := s.Since(ctx, "web", "c1", first, 100)
	if err != nil {
		t.Fatalf("since after first: %v", err)
	}
	if len(after) != 1 || after[0].Content != "b" {
		t.Fatalf("after-first = %+v", after)
	}
}

func TestSinceExpiredCursor(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	var ids []string
	for i := 0; i < 5; i++ {
		id, err := s.Append(ctx, Event{Channel: "web", ChatID: "c1", Type: "chunk", Content: "x"})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		ids = append(ids, id)
	}
	if err := s.Trim(ctx, "web", "c1", 2); err != nil {
		t.Fatalf("trim: %v", err)
	}

	if _, err := s.Since(ctx, "web", "c1", ids[0], 100); err != ErrCursorExpired {
		t.Fatalf("err = %v, want ErrCursorExpired", err)
	}
	// The newest id is still current: nothing after it, and no reset.
	if events, err := s.Since(ctx, "web", "c1", ids[len(ids)-1], 100); err != nil || len(events) != 0 {
		t.Fatalf("events = %+v, err = %v", events, err)
	}
}

func TestTrimKeepsNewest(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		if _, err := s.Append(ctx, Event{Channel: "web", ChatID: "c1", Type: "chunk"}); err != nil {
			t.Fatalf("append: %v", err)
		}
		// Keep ids distinguishable by order; the content is not what this checks.
		time.Sleep(time.Millisecond)
	}
	if err := s.Trim(ctx, "web", "c1", 2); err != nil {
		t.Fatalf("trim: %v", err)
	}
	events, err := s.Since(ctx, "web", "c1", "", 100)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("kept %d events, want 2", len(events))
	}
}

func TestAppendRequiresChatID(t *testing.T) {
	s := testStore(t)
	if _, err := s.Append(context.Background(), Event{Channel: "web", Type: "chunk"}); err == nil {
		t.Fatal("an event without chat_id was accepted")
	}
}
