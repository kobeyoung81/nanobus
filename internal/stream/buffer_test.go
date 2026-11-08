package stream

import "testing"

func TestRingBuffer_AppendAndEventsFrom(t *testing.T) {
	rb := NewRingBuffer(3)

	rb.Append(ChunkEvent{Type: "chunk", Content: "a"})
	id2 := rb.Append(ChunkEvent{Type: "chunk", Content: "b"})
	rb.Append(ChunkEvent{Type: "chunk", Content: "c"})

	events := rb.EventsFrom(0)
	if len(events) != 3 {
		t.Fatalf("EventsFrom(0) len = %d, want 3", len(events))
	}

	events = rb.EventsFrom(id2)
	if len(events) != 1 {
		t.Fatalf("EventsFrom(%d) len = %d, want 1", id2, len(events))
	}
	if events[0].Content != "c" {
		t.Errorf("events[0].Content = %q, want %q", events[0].Content, "c")
	}
}

func TestRingBuffer_Truncation(t *testing.T) {
	rb := NewRingBuffer(3)
	for _, c := range []string{"1", "2", "3", "4", "5"} {
		rb.Append(ChunkEvent{Type: "chunk", Content: c})
	}

	events := rb.EventsFrom(0)
	if len(events) != 3 {
		t.Fatalf("EventsFrom(0) len = %d, want 3 (truncated to capacity)", len(events))
	}
	if events[0].Content != "3" {
		t.Errorf("oldest content = %q, want %q", events[0].Content, "3")
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	rb := NewRingBuffer(10)
	if events := rb.EventsFrom(0); events != nil && len(events) != 0 {
		t.Fatalf("expected no events, got %d", len(events))
	}
}

func TestRingBuffer_MonotonicIDs(t *testing.T) {
	rb := NewRingBuffer(10)
	ids := []int{}
	for i := 0; i < 5; i++ {
		ids = append(ids, rb.Append(ChunkEvent{Type: "chunk"}))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("IDs not monotonic: %v", ids)
		}
	}
}
