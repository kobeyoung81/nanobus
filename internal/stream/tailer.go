package stream

// Read tailing.
//
// Before this, every SSE subscriber polled the collection every 250 ms: fifty
// open conversations meant two hundred queries a second, and the database load
// grew with the number of connected clients. A replica now runs one tailer per
// collection — a change stream for latency, plus a slow scan as a safety net —
// and fans the events out to its own subscribers. Database reads are per
// replica, so they no longer depend on how many streams are open.
//
// A subscriber that cannot keep up is not silently fed a hole: its buffer
// overflows and it is told so (`Lag`), and the reader catches up from the log.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	// tailBuffer is how many events one subscriber may fall behind before it is
	// told to catch up from the log instead.
	tailBuffer = 512
	// tailScanEvery is the safety net's period: one query per collection.
	tailScanEvery = 2 * time.Second
	// tailScanPage bounds one scan; a longer backlog is read over several ticks.
	tailScanPage = 500
)

// Tailer follows one collection and fans new documents out to local
// subscribers.
type Tailer[T any] struct {
	coll      *mongo.Collection
	decode    func(bson.Raw) (T, bool)
	idOf      func(T) string
	subjectOf func(T) string
	scan      func(context.Context, string, int) ([]T, error)
	newest    func(context.Context) (string, error)
	scanEvery time.Duration

	mu     sync.Mutex
	nextID int
	subs   map[int]*TailSub[T]
	cursor string
}

// TailSub is one subscriber's view of a tailer.
type TailSub[T any] struct {
	tailer  *Tailer[T]
	id      int
	subject string
	ch      chan T
	lag     chan struct{}
}

// Events delivers the events this subscriber asked for.
func (s *TailSub[T]) Events() <-chan T { return s.ch }

// Lag is signalled when events were dropped for this subscriber: whatever was
// missed is still in the log and must be read from there.
func (s *TailSub[T]) Lag() <-chan struct{} { return s.lag }

// Close removes the subscriber.
func (s *TailSub[T]) Close() { s.tailer.unsubscribe(s.id) }

// NewEventTailer tails the conversation log.
func NewEventTailer(st *Store) *Tailer[Event] {
	return &Tailer[Event]{
		coll:   st.coll,
		decode: decodeEvent,
		idOf:   func(ev Event) string { return ev.ID.Hex() },
		subjectOf: func(ev Event) string {
			return conversationKey(ev.Channel, ev.ChatID)
		},
		scan:      st.scanAll,
		newest:    st.newestEvent,
		scanEvery: tailScanEvery,
		subs:      make(map[int]*TailSub[Event]),
	}
}

// NewPlatformTailer tails the notification log.
func NewPlatformTailer(st *PlatformStore) *Tailer[PlatformEvent] {
	return &Tailer[PlatformEvent]{
		coll:   st.coll,
		decode: decodePlatformEvent,
		idOf:   func(ev PlatformEvent) string { return ev.ID.Hex() },
		// Notifications are not partitioned: every subscriber gets every one.
		subjectOf: func(PlatformEvent) string { return "" },
		scan:      st.Since,
		newest:    st.Newest,
		scanEvery: tailScanEvery,
		subs:      make(map[int]*TailSub[PlatformEvent]),
	}
}

func decodeEvent(raw bson.Raw) (Event, bool) {
	var ev Event
	if err := bson.Unmarshal(raw, &ev); err != nil {
		return ev, false
	}
	return ev, ev.ChatID != ""
}

func decodePlatformEvent(raw bson.Raw) (PlatformEvent, bool) {
	var ev PlatformEvent
	if err := bson.Unmarshal(raw, &ev); err != nil {
		return ev, false
	}
	return ev, ev.Type != ""
}

// Subscribe registers a subscriber for one subject ("" means everything).
func (t *Tailer[T]) Subscribe(subject string) *TailSub[T] {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	sub := &TailSub[T]{
		tailer:  t,
		id:      t.nextID,
		subject: subject,
		ch:      make(chan T, tailBuffer),
		lag:     make(chan struct{}, 1),
	}
	t.subs[sub.id] = sub
	return sub
}

func (t *Tailer[T]) unsubscribe(id int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.subs, id)
}

// Cursor is the highest event id this replica has fanned out.
func (t *Tailer[T]) Cursor() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cursor
}

// Run tails until the context is cancelled: the change stream is the fast path,
// the periodic scan covers a stream that died or never started (a standalone
// mongod has no change streams at all).
func (t *Tailer[T]) Run(ctx context.Context) {
	if t.newest != nil {
		if id, err := t.newest(ctx); err == nil && id != "" {
			t.mu.Lock()
			t.cursor = id
			t.mu.Unlock()
		}
	}
	go t.scanLoop(ctx)

	var resume bson.Raw
	backoff := 200 * time.Millisecond
	for ctx.Err() == nil {
		stream, err := t.watch(ctx, resume)
		if err != nil {
			slog.Warn("stream: tailer could not open the change stream; relying on the scan", "error", err)
			sleep(ctx, time.Second)
			continue
		}
		slog.Info("stream: tailer following the change stream")
		for stream.Next(ctx) {
			resume = stream.ResumeToken()
			var change struct {
				FullDocument bson.Raw `bson:"fullDocument"`
			}
			if err := stream.Decode(&change); err != nil {
				continue
			}
			item, ok := t.decode(change.FullDocument)
			if !ok {
				continue
			}
			t.publishOne(item)
		}
		if err := stream.Err(); err != nil && ctx.Err() == nil {
			slog.Warn("stream: tailer lost the change stream; resuming", "error", err)
		}
		_ = stream.Close(context.Background())
		sleep(ctx, backoff)
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// watch opens a change stream filtered to inserts.
func (t *Tailer[T]) watch(ctx context.Context, resume bson.Raw) (*mongo.ChangeStream, error) {
	pipeline := mongo.Pipeline{{{Key: "$match", Value: bson.D{{Key: "operationType", Value: "insert"}}}}}
	opts := options.ChangeStream()
	if len(resume) > 0 {
		opts = opts.SetResumeAfter(resume)
	}
	return t.coll.Watch(ctx, pipeline, opts)
}

// scanLoop publishes anything the change stream did not deliver. One query per
// period per replica, whatever the subscriber count.
func (t *Tailer[T]) scanLoop(ctx context.Context) {
	period := t.scanEvery
	if period <= 0 {
		period = tailScanEvery
	}
	ticker := time.NewTicker(period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < 10; i++ {
				cursor := t.Cursor()
				if cursor == "" {
					break
				}
				items, err := t.scan(ctx, cursor, tailScanPage)
				if err != nil {
					slog.Warn("stream: tailer scan failed", "error", err)
					break
				}
				if len(items) == 0 {
					break
				}
				for _, item := range items {
					t.publishOne(item)
				}
				if len(items) < tailScanPage {
					break
				}
			}
		}
	}
}

// publishOne fans one event out, skipping anything at or behind the cursor so a
// change-stream delivery and a scan of the same row are sent once.
func (t *Tailer[T]) publishOne(item T) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := t.idOf(item)
	if id == "" || (t.cursor != "" && id <= t.cursor) {
		return
	}
	t.cursor = id
	subject := t.subjectOf(item)
	for _, sub := range t.subs {
		if subject != "" && sub.subject != subject {
			continue
		}
		select {
		case sub.ch <- item:
		default:
			select {
			case sub.lag <- struct{}{}:
			default:
			}
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
