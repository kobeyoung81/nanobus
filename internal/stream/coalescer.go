package stream

// Write coalescing and batching.
//
// A turn emits several chunks per second; writing each one as its own document
// doubles the database work for no reader benefit. Chunks of the same message
// and kind are therefore merged inside a short window (the design's 150–250 ms)
// into one row: content is concatenated in order, `seq` keeps the last value.
// Merged rows from every conversation are then handed to the database in one
// `insertMany` every 20 ms, so a replica with many busy conversations still
// spends one round trip per window.
//
// Three rules make that safe:
//   - a `complete` flushes the conversation's pending chunk first, so the
//     stored order never puts an answer after its own completion;
//   - a change of message or kind flushes the previous buffer, so merged rows
//     never mix kinds (thinking with content, one message with another);
//   - a completion is written before the caller returns: the gateway closes its
//     subscription on that event, and a lost completion would strand it.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/metrics"
)

const (
	defaultMergeWindow = 200 * time.Millisecond
	defaultBatchWindow = 20 * time.Millisecond
)

// Appender is the write side of the event log (the store, or a fake in tests).
type Appender interface {
	Append(ctx context.Context, ev Event) (string, error)
}

// BatchAppender writes several events in one round trip. A store that implements
// it gets batched writes; a fake that does not keeps one call per event.
type BatchAppender interface {
	AppendMany(ctx context.Context, evs []Event) error
}

// Coalescer merges chunk writes per conversation and batches the results.
type Coalescer struct {
	store       Appender
	batch       BatchAppender
	window      time.Duration
	batchWindow time.Duration

	mu      sync.Mutex
	pending map[string]*pendingBatch
	queue   []Event
	timer   *time.Timer
	closed  bool
}

type pendingBatch struct {
	ev    Event
	timer *time.Timer
}

// NewCoalescer builds a coalescer with the given merge window.
func NewCoalescer(store Appender, window time.Duration) *Coalescer {
	if window <= 0 {
		window = defaultMergeWindow
	}
	c := &Coalescer{
		store:       store,
		window:      window,
		batchWindow: defaultBatchWindow,
		pending:     make(map[string]*pendingBatch),
	}
	if b, ok := store.(BatchAppender); ok {
		c.batch = b
	}
	return c
}

// Add accepts one event. Chunks are buffered; a complete flushes the buffer and
// is written before Add returns so the client sees the end of the turn promptly.
func (c *Coalescer) Add(ev Event) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if ev.Type == "complete" {
		pending := c.takeLocked(conversationKey(ev.Channel, ev.ChatID))
		c.mu.Unlock()
		if pending != nil {
			c.enqueue(pending.ev)
		}
		// The chunk must be durable before its completion, and the completion
		// before the caller answers: drain now instead of waiting for the window.
		c.flushNow()
		c.writeMany([]Event{ev})
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
		if due != nil {
			c.enqueue(due.ev)
		}
	})
	c.pending[key] = batch
	c.mu.Unlock()

	if previous != nil {
		c.enqueue(previous.ev)
	}
}

// FlushAll writes everything buffered and stops accepting new events.
func (c *Coalescer) FlushAll() {
	c.mu.Lock()
	c.closed = true
	events := c.queue
	c.queue = nil
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	for key := range c.pending {
		if batch := c.takeLocked(key); batch != nil {
			events = append(events, batch.ev)
		}
	}
	c.mu.Unlock()
	c.writeMany(events)
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

// enqueue adds one event to the write batch and arms the batching timer.
func (c *Coalescer) enqueue(ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.queue = append(c.queue, ev)
	if c.timer == nil {
		c.timer = time.AfterFunc(c.batchWindow, c.flushNow)
	}
}

// flushNow writes whatever is queued and stops the timer.
func (c *Coalescer) flushNow() {
	c.mu.Lock()
	events := c.queue
	c.queue = nil
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.mu.Unlock()
	c.writeMany(events)
}

func (c *Coalescer) writeMany(events []Event) {
	if len(events) == 0 {
		return
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if c.batch != nil {
		if err := c.batch.AppendMany(ctx, events); err != nil {
			slog.Warn("stream: batched write failed", "events", len(events), "error", err)
		}
		metrics.StreamWriteSeconds.Observe(time.Since(start).Seconds())
		return
	}
	for _, ev := range events {
		if _, err := c.store.Append(ctx, ev); err != nil {
			slog.Warn("stream: coalesced write failed", "channel", ev.Channel, "chat_id", ev.ChatID, "error", err)
		}
	}
	metrics.StreamWriteSeconds.Observe(time.Since(start).Seconds())
}
