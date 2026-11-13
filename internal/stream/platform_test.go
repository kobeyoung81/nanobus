package stream

// The change-notification log is what lets a subscriber on one replica see a
// change published on another. Ordering and trimming are server behaviours, so
// these run against a real MongoDB and skip when none is configured.

import (
	"context"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func TestPlatformAppendSinceNewest(t *testing.T) {
	st := storetest.Connect(t)
	s := NewPlatformStore(st.DB())
	ctx := context.Background()

	first, err := s.Append(ctx, "config_changed", `{"keys":["agent.default.model"]}`)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	second, err := s.Append(ctx, "agents_changed", `{"user_id":"u1"}`)
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	events, err := s.Since(ctx, "", 100)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(events) != 2 || events[0].Type != "config_changed" || events[1].Type != "agents_changed" {
		t.Fatalf("events = %+v", events)
	}
	if events[0].ID.Hex() != first || events[1].ID.Hex() != second {
		t.Fatalf("ids do not match the appends")
	}

	newest, err := s.Newest(ctx)
	if err != nil || newest != second {
		t.Fatalf("newest = %q, err = %v, want %q", newest, err, second)
	}

	after, err := s.Since(ctx, first, 100)
	if err != nil {
		t.Fatalf("since after first: %v", err)
	}
	if len(after) != 1 || after[0].Type != "agents_changed" {
		t.Fatalf("after = %+v", after)
	}
}

func TestPlatformTrimKeepsNewest(t *testing.T) {
	st := storetest.Connect(t)
	s := NewPlatformStore(st.DB())
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		if _, err := s.Append(ctx, "config_changed", "x"); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := s.Trim(ctx, 2); err != nil {
		t.Fatalf("trim: %v", err)
	}
	events, err := s.Since(ctx, "", 100)
	if err != nil {
		t.Fatalf("since: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("kept %d events, want 2", len(events))
	}
}

func TestPlatformSinceRejectsAGarbageCursor(t *testing.T) {
	st := storetest.Connect(t)
	if _, err := NewPlatformStore(st.DB()).Since(context.Background(), "nope", 10); err == nil {
		t.Fatal("a garbage cursor was accepted")
	}
}
