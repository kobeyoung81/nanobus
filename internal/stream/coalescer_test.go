package stream

// The coalescer exists so a turn's token stream does not become one database
// document per token. What it must never change: the stored order (a completion
// after its answer) and the separation of kinds.

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordedAppends struct {
	mu     sync.Mutex
	events []Event
}

func (r *recordedAppends) Append(_ context.Context, ev Event) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return "id", nil
}

func (r *recordedAppends) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

func TestCoalescerMergesChunksWithinTheWindow(t *testing.T) {
	rec := &recordedAppends{}
	c := NewCoalescer(rec, 30*time.Millisecond)

	c.Add(Event{Channel: "web", ChatID: "c1", Type: "chunk", Kind: "content", MessageID: "m1", Seq: 1, Content: "he"})
	c.Add(Event{Channel: "web", ChatID: "c1", Type: "chunk", Kind: "content", MessageID: "m1", Seq: 2, Content: "llo"})
	time.Sleep(80 * time.Millisecond)

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("appends = %d, want 1 merged write", len(events))
	}
	if events[0].Content != "hello" || events[0].Seq != 2 {
		t.Fatalf("merged event = %+v", events[0])
	}
}

func TestCoalescerFlushesPendingBeforeComplete(t *testing.T) {
	rec := &recordedAppends{}
	c := NewCoalescer(rec, time.Minute) // only the complete may flush

	c.Add(Event{Channel: "web", ChatID: "c1", Type: "chunk", Kind: "content", MessageID: "m1", Content: "hi"})
	c.Add(Event{Channel: "web", ChatID: "c1", Type: "complete", MessageID: "m1", FullContent: "hi"})

	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("appends = %d, want the chunk then the completion", len(events))
	}
	if events[0].Type != "chunk" || events[1].Type != "complete" {
		t.Fatalf("stored order = %v, %v", events[0].Type, events[1].Type)
	}
}

func TestCoalescerSeparatesKinds(t *testing.T) {
	rec := &recordedAppends{}
	c := NewCoalescer(rec, time.Minute)

	c.Add(Event{Channel: "web", ChatID: "c1", Type: "chunk", Kind: "thought", MessageID: "m1", Content: "thinking"})
	c.Add(Event{Channel: "web", ChatID: "c1", Type: "chunk", Kind: "content", MessageID: "m1", Content: "answer"})
	c.FlushAll()

	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("appends = %d, want one per kind", len(events))
	}
	if events[0].Kind != "thought" || events[1].Kind != "content" {
		t.Fatalf("kinds = %q, %q", events[0].Kind, events[1].Kind)
	}
}

func TestCoalescerFlushAllWritesEverything(t *testing.T) {
	rec := &recordedAppends{}
	c := NewCoalescer(rec, time.Minute)
	c.Add(Event{Channel: "web", ChatID: "c1", Type: "chunk", Kind: "content", MessageID: "m1", Content: "a"})
	c.Add(Event{Channel: "web", ChatID: "c2", Type: "chunk", Kind: "content", MessageID: "m2", Content: "b"})
	c.FlushAll()

	if len(rec.all()) != 2 {
		t.Fatalf("appends = %d, want both conversations flushed", len(rec.all()))
	}
	// After a flush the coalescer is closed: later events are not silently
	// buffered forever.
	c.Add(Event{Channel: "web", ChatID: "c3", Type: "chunk", MessageID: "m3", Content: "c"})
	if len(rec.all()) != 2 {
		t.Fatalf("a closed coalescer accepted more events")
	}
}

// batchedAppends records AppendMany calls, keeping the order inside each batch.
type batchedAppends struct {
	mu      sync.Mutex
	batches [][]Event
}

func (b *batchedAppends) Append(_ context.Context, ev Event) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batches = append(b.batches, []Event{ev})
	return "id", nil
}

func (b *batchedAppends) AppendMany(_ context.Context, evs []Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batches = append(b.batches, append([]Event(nil), evs...))
	return nil
}

func (b *batchedAppends) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.batches)
}

func (b *batchedAppends) flat() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Event
	for _, batch := range b.batches {
		out = append(out, batch...)
	}
	return out
}

// TestCoalescerBatchesAcrossConversations: merged rows from different
// conversations leave in one insertMany per window, not one round trip each.
func TestCoalescerBatchesAcrossConversations(t *testing.T) {
	rec := &batchedAppends{}
	c := NewCoalescer(rec, 5*time.Millisecond)

	c.Add(Event{Channel: "web", ChatID: "c1", Type: "chunk", Kind: "content", MessageID: "m1", Content: "a"})
	c.Add(Event{Channel: "web", ChatID: "c2", Type: "chunk", Kind: "content", MessageID: "m2", Content: "b"})
	c.Add(Event{Channel: "web", ChatID: "c3", Type: "chunk", Kind: "content", MessageID: "m3", Content: "c"})
	time.Sleep(80 * time.Millisecond)

	if got := rec.calls(); got != 1 {
		t.Fatalf("database calls = %d, want one batch", got)
	}
	if got := len(rec.flat()); got != 3 {
		t.Fatalf("events written = %d, want 3", got)
	}
}

// TestCoalescerCompletionIsDurableBeforeReturn: the gateway closes its
// subscription on the completion, so both it and the answer before it must be
// written by the time Add returns.
func TestCoalescerCompletionIsDurableBeforeReturn(t *testing.T) {
	rec := &batchedAppends{}
	c := NewCoalescer(rec, time.Minute) // only the completion may flush

	c.Add(Event{Channel: "web", ChatID: "c1", Type: "chunk", Kind: "content", MessageID: "m1", Content: "hi"})
	c.Add(Event{Channel: "web", ChatID: "c1", Type: "complete", MessageID: "m1", FullContent: "hi"})

	events := rec.flat()
	if len(events) != 2 {
		t.Fatalf("stored %d events when Add returned, want chunk + completion", len(events))
	}
	if events[0].Type != "chunk" || events[1].Type != "complete" {
		t.Fatalf("stored order = %v, %v", events[0].Type, events[1].Type)
	}
}
