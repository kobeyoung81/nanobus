// Package stream implements the in-memory chunk ring buffer used for SSE
// streaming delivery of outbound chunks from agents to gateways.
package stream

import "sync"

// ChunkEvent is a single streaming event delivered over SSE.
type ChunkEvent struct {
	ID          int    `json:"id"`
	Type        string `json:"type"`           // "chunk" | "complete"
	Kind        string `json:"kind,omitempty"` // content | thought | tool_use | tool_result
	MessageID   string `json:"message_id,omitempty"`
	Seq         int    `json:"seq,omitempty"`
	Content     string `json:"content,omitempty"`
	FullContent string `json:"full_content,omitempty"`
	Summary     string `json:"summary,omitempty"`
	// Error carries a nanoprotocol.TurnError* category on "complete" events
	// when the turn failed; ErrorDetail is a sanitized human supplement.
	Error       string `json:"error,omitempty"`
	ErrorDetail string `json:"error_detail,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	Channel     string `json:"channel,omitempty"`
	ChatID      string `json:"chat_id,omitempty"`
	SessionID   string `json:"session_id,omitempty"`
}

// RingBuffer is a bounded, ordered history of events with globally monotonic IDs.
type RingBuffer struct {
	mu       sync.Mutex
	events   []ChunkEvent // in ascending ID order
	capacity int
	nextID   int
}

// NewRingBuffer creates a ring buffer with the given capacity (default 1000).
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1000
	}
	return &RingBuffer{
		events:   make([]ChunkEvent, 0, capacity),
		capacity: capacity,
		nextID:   1,
	}
}

// Append stores an event and returns its assigned ID.
func (r *RingBuffer) Append(ev ChunkEvent) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	ev.ID = r.nextID
	r.nextID++
	r.events = append(r.events, ev)
	if len(r.events) > r.capacity {
		r.events = r.events[len(r.events)-r.capacity:]
	}
	return ev.ID
}

// Newest returns the ID most recently handed out (0 before the first event), so a
// caller can tell whether a client's resume point was issued by this ring at all.
func (r *RingBuffer) Newest() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nextID - 1
}

// EventsFrom returns all events with ID greater than lastID, in order.
// If lastID is 0 it returns the oldest buffered event onward.
func (r *RingBuffer) EventsFrom(lastID int) []ChunkEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return nil
	}
	start := 0
	for i, ev := range r.events {
		if ev.ID > lastID {
			start = i
			break
		}
	}
	if start >= len(r.events) {
		return nil
	}
	out := make([]ChunkEvent, len(r.events)-start)
	copy(out, r.events[start:])
	return out
}

// ChunkBuffer holds a ring buffer per channel.
type ChunkBuffer struct {
	mu       sync.RWMutex
	channels map[string]*RingBuffer
	capacity int
}

// NewChunkBuffer creates a chunk buffer for all channels.
func NewChunkBuffer(capacity int) *ChunkBuffer {
	return &ChunkBuffer{
		channels: make(map[string]*RingBuffer),
		capacity: capacity,
	}
}

// Channel returns the ring buffer for a channel, creating it if needed.
func (b *ChunkBuffer) Channel(channel string) *RingBuffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	rb, ok := b.channels[channel]
	if !ok {
		rb = NewRingBuffer(b.capacity)
		b.channels[channel] = rb
	}
	return rb
}
