package server

// The conversation stream is what makes replay work across a reconnect and
// across replicas: the SSE route reads the durable log instead of a process
// ring. These tests drive the HTTP surface with a scripted store so the cursor
// handling (replay, reset) is observable.

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/stream"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type fakeStreamStore struct {
	mu       sync.Mutex
	appended []stream.Event
	// steps is a queue of answers for Since; the last one repeats.
	steps []func(afterID string) ([]stream.Event, error)
	calls []string
}

func (f *fakeStreamStore) Append(_ context.Context, ev stream.Event) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appended = append(f.appended, ev)
	return bson.NewObjectID().Hex(), nil
}

func (f *fakeStreamStore) Since(_ context.Context, _, _, afterID string, _ int) ([]stream.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, afterID)
	step := f.steps[0]
	if len(f.steps) > 1 {
		f.steps = f.steps[1:]
	}
	return step(afterID)
}

func streamTestServer(t *testing.T, fake *fakeStreamStore) *httptest.Server {
	t.Helper()
	s := NewServer(nil)
	s.streams = fake
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestChunkRequiresChatID: the key of the event log is the conversation, so an
// event without one cannot be stored or replayed.
func TestChunkRequiresChatID(t *testing.T) {
	fake := &fakeStreamStore{steps: []func(string) ([]stream.Event, error){func(string) ([]stream.Event, error) { return nil, nil }}}
	srv := streamTestServer(t, fake)

	resp, err := http.Post(srv.URL+"/api/v1/outbound/chunk", "application/json",
		strings.NewReader(`{"channel":"web","content":"hi"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if len(fake.appended) != 0 {
		t.Fatal("a chunk without chat_id was persisted")
	}
}

func TestChunkIsPersistedWithItsConversation(t *testing.T) {
	fake := &fakeStreamStore{steps: []func(string) ([]stream.Event, error){func(string) ([]stream.Event, error) { return nil, nil }}}
	srv := streamTestServer(t, fake)

	resp, err := http.Post(srv.URL+"/api/v1/outbound/chunk", "application/json",
		strings.NewReader(`{"channel":"web","chat_id":"c1","agent_id":"a1","content":"hi"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if len(fake.appended) != 1 || fake.appended[0].ChatID != "c1" || fake.appended[0].Content != "hi" {
		t.Fatalf("persisted = %+v", fake.appended)
	}
}

// TestConversationStreamReplaysAndResets: the first poll returns the missed
// events, the next reports an expired cursor, and the route turns that into a
// reset event instead of silently skipping the gap.
func TestConversationStreamReplaysAndResets(t *testing.T) {
	old := bson.NewObjectID()
	fresh := bson.NewObjectID()
	fake := &fakeStreamStore{steps: []func(string) ([]stream.Event, error){
		func(after string) ([]stream.Event, error) {
			return []stream.Event{{
				ID: fresh, Channel: "web", ChatID: "c1", Type: "chunk", Content: "hello",
			}}, nil
		},
		func(string) ([]stream.Event, error) { return nil, stream.ErrCursorExpired },
		func(string) ([]stream.Event, error) { return nil, nil },
	}}
	srv := streamTestServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/api/v1/outbound/stream?channel=web&chat_id=c1", nil)
	req.Header.Set("Last-Event-ID", old.Hex())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	var sawChunk, sawReset bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "event: chunk":
			sawChunk = true
		case strings.HasPrefix(line, "id: "+fresh.Hex()):
			if !sawChunk {
				t.Fatal("the event id arrived before its type")
			}
		case line == "event: reset":
			sawReset = true
		case strings.HasPrefix(line, "data: {") && strings.Contains(line, `"reason":"cursor_expired"`):
			if !sawReset {
				t.Fatal("the reason arrived before the reset event")
			}
		}
		if sawChunk && sawReset {
			break
		}
	}
	if !sawChunk {
		t.Fatal("the missed event was not replayed")
	}
	if !sawReset {
		t.Fatal("an expired cursor did not produce a reset event")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) == 0 || fake.calls[0] != old.Hex() {
		t.Fatalf("the first poll did not resume from the client's cursor: %v", fake.calls)
	}
}

// TestConversationStreamRejectsAGarbageCursor keeps a malformed id from being
// treated as "start over silently": the client is told to reset.
func TestConversationStreamRejectsAGarbageCursor(t *testing.T) {
	fake := &fakeStreamStore{steps: []func(string) ([]stream.Event, error){
		func(string) ([]stream.Event, error) { return nil, nil },
	}}
	srv := streamTestServer(t, fake)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/api/v1/outbound/stream?channel=web&chat_id=c1", nil)
	req.Header.Set("Last-Event-ID", "not-an-object-id")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), `"reason":"invalid_cursor"`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("a malformed cursor did not produce a reset")
	}
}

// TestConversationStreamNeedsAStore: without a database the route fails loudly
// instead of pretending to stream nothing.
func TestConversationStreamNeedsAStore(t *testing.T) {
	s := NewServer(nil)
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/outbound/stream?channel=web&chat_id=c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}
