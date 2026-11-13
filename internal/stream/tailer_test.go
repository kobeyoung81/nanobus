package stream

// One reader per replica: these tests pin the fan-out rules that replaced the
// per-subscriber polling — subject filtering, the monotonic cursor (a change
// stream delivery and a scan of the same row are sent once), and the lag signal
// that keeps a slow subscriber from either blocking the tailer or losing events
// silently.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func testEvent(id string, channel, chatID, content string) Event {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		panic(err)
	}
	return Event{ID: oid, Channel: channel, ChatID: chatID, Type: "chunk", Content: content}
}

func newTestTailer(scan func(context.Context, string, int) ([]Event, error)) *Tailer[Event] {
	return &Tailer[Event]{
		idOf: func(ev Event) string { return ev.ID.Hex() },
		subjectOf: func(ev Event) string {
			return conversationKey(ev.Channel, ev.ChatID)
		},
		scan:      scan,
		subs:      make(map[int]*TailSub[Event]),
		scanEvery: 10 * time.Millisecond,
	}
}

func TestTailerFansOutByConversation(t *testing.T) {
	tailer := newTestTailer(nil)
	t1 := tailer.Subscribe(conversationKey("web", "chat-1"))
	t2 := tailer.Subscribe(conversationKey("web", "chat-2"))
	defer t1.Close()
	defer t2.Close()

	tailer.publishOne(testEvent("64b000000000000000000001", "web", "chat-1", "one"))
	tailer.publishOne(testEvent("64b000000000000000000002", "web", "chat-2", "two"))

	select {
	case ev := <-t1.Events():
		if ev.Content != "one" {
			t.Fatalf("chat-1 got %q", ev.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("chat-1 received nothing")
	}
	select {
	case ev := <-t2.Events():
		if ev.Content != "two" {
			t.Fatalf("chat-2 got %q", ev.Content)
		}
	case <-time.After(time.Second):
		t.Fatal("chat-2 received nothing")
	}
	select {
	case ev := <-t1.Events():
		t.Fatalf("chat-1 received another conversation's event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTailerSendsEachRowOnce(t *testing.T) {
	tailer := newTestTailer(nil)
	sub := tailer.Subscribe(conversationKey("web", "chat-1"))
	defer sub.Close()

	first := testEvent("64b000000000000000000010", "web", "chat-1", "first")
	tailer.publishOne(first)
	// The change stream and the scan can both see the same row.
	tailer.publishOne(first)
	// An older row arriving late must not be replayed out of order.
	tailer.publishOne(testEvent("64b00000000000000000000f", "web", "chat-1", "older"))

	if ev := <-sub.Events(); ev.Content != "first" {
		t.Fatalf("got %q", ev.Content)
	}
	select {
	case ev := <-sub.Events():
		t.Fatalf("duplicate or out-of-order event delivered: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	if got := tailer.Cursor(); got != first.ID.Hex() {
		t.Fatalf("cursor = %q, want %q", got, first.ID.Hex())
	}
}

func TestTailerSignalsLagInsteadOfBlockingOrDroppingSilently(t *testing.T) {
	tailer := newTestTailer(nil)
	sub := tailer.Subscribe(conversationKey("web", "chat-1"))
	defer sub.Close()

	// One more than the buffer: the last event cannot be queued.
	for i := 0; i <= tailBuffer; i++ {
		id := bson.NewObjectID()
		tailer.publishOne(Event{ID: id, Channel: "web", ChatID: "chat-1", Type: "chunk", Content: "x"})
	}
	select {
	case <-sub.Lag():
	case <-time.After(time.Second):
		t.Fatal("a subscriber that fell behind was not told")
	}
}

func TestTailerScanPublishesWhatTheStreamMissed(t *testing.T) {
	rows := []Event{
		testEvent("64b000000000000000000020", "web", "chat-1", "a"),
		testEvent("64b000000000000000000021", "web", "chat-1", "b"),
	}
	scans := make(chan string, 4)
	tailer := newTestTailer(func(_ context.Context, afterID string, _ int) ([]Event, error) {
		scans <- afterID
		var out []Event
		for _, ev := range rows {
			if ev.ID.Hex() > afterID {
				out = append(out, ev)
			}
		}
		return out, nil
	})
	tailer.cursor = "64b000000000000000000000"
	sub := tailer.Subscribe(conversationKey("web", "chat-1"))
	defer sub.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tailer.scanLoop(ctx)

	for i := 0; i < 2; i++ {
		select {
		case ev := <-sub.Events():
			if ev.Content != rows[i].Content {
				t.Fatalf("event %d = %q", i, ev.Content)
			}
		case <-time.After(time.Second):
			t.Fatalf("the scan delivered only %d event(s)", i)
		}
	}
	select {
	case after := <-scans:
		if after == "" {
			t.Fatal("the scan ran without a cursor")
		}
	default:
		t.Fatal("the scan never ran")
	}
}
