package stream

// Write coalescing.
//
// A turn emits several chunks per second; writing each one as its own document
// doubles the database work for no reader benefit. Chunks of the same message
// and kind are therefore merged inside a short window (the design's 150–250 ms)
// into one row: content is concatenated in order, `seq` keeps the last value.
//
// Two rules make that safe:
//   - a `complete` flushes the conversation's pending chunk first, so the
//     stored order never puts an answer after its own completion;
//   - a change of message or kind flushes the previous buffer, so merged rows
//     never mix kinds (thinking with content, one message with another).

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/metrics"
)

// Appender is the write side of the event log (the store, or a fake in tests).
type Appender interface {
	Append(ctx context.Context, ev Event) (string, error)
}

// Coalescer batches chunk writes per conversation.
type Coalescer struct {
	store  Appender
	window time.Duration

	mu      sync.Mutex
	pending map[string]*pendingBatch
	closed  bool
}

type pendingBatch struct {
	ev    Event
	timer *time.Timer
}

// NewCoalescer builds a coalescer with the given window.
func NewCoalescer(store Appender, window time.Duration) *Coalescer {
	if window <= 0 {
		window = 200 * time.Millisecond
	}
	return &Coalescer{store: store, window: window, pending: make(map[string]*pendingBatch)}
}

// Add accepts one event. Chunks are buffered; a complete flushes the buffer and
// is written immediately so the client sees the end of the turn promptly.
func (c *Coalescer) Add(ev Event) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if ev.Type == "complete" {
		pending := c.takeLocked(conversationKey(ev.Channel, ev.ChatID))
		c.mu.Unlock()
		c.write(pending)
		c.write(&pendingBatch{ev: ev})
		return
	}

	key := conversationKey(ev.Channel, ev.ChatID)
	batch, ok := c.pending[key]
	if ok && batch.ev.MessageID == ev.MessageID && batch.ev.Kind == ev.Kind {
		batch.ev.Content += ev.Content
		batch.ev.Seq = ev.Seq
		c.mu.Unlock()
		return
	}
	var previous *pendingBatch
	if ok {
		previous = c.takeLocked(key)
	}
	batch = &pendingBatch{ev: ev}
	batch.timer = time.AfterFunc(c.window, func() {
		c.mu.Lock()
		due := c.takeLocked(key)
		c.mu.Unlock()
		c.write(due)
	})
	c.pending[key] = batch
	c.mu.Unlock()

	c.write(previous)
}

// FlushAll writes everything buffered and stops accepting new events.
func (c *Coalescer) FlushAll() {
	c.mu.Lock()
	c.closed = true
	batches := make([]*pendingBatch, 0, len(c.pending))
	for key := range c.pending {
		batches = append(batches, c.takeLocked(key))
	}
	c.mu.Unlock()
	for _, batch := range batches {
		c.write(batch)
	}
}

func (c *Coalescer) takeLocked(key string) *pendingBatch {
	batch, ok := c.pending[key]
	if !ok {
		return nil
	}
	delete(c.pending, key)
	if batch.timer != nil {
		batch.timer.Stop()
	}
	return batch
}

func (c *Coalescer) write(batch *pendingBatch) {
	if batch == nil {
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := c.store.Append(ctx, batch.ev)
	cancel()
	metrics.StreamWriteSeconds.Observe(time.Since(start).Seconds())
	if err != nil {
		slog.Warn("stream: coalesced write failed", "channel", batch.ev.Channel, "chat_id", batch.ev.ChatID, "error", err)
	}
}
