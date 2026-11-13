package server

// Durable platform events and voice ids: a second replica must serve what the
// first one published or minted.

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

type fakePlatformEvents struct {
	mu      sync.Mutex
	newest  string
	events  []stream.PlatformEvent
	cursors []string
}

func (f *fakePlatformEvents) Append(context.Context, string, string) (string, error) {
	return "", nil
}

func (f *fakePlatformEvents) Newest(context.Context) (string, error) { return f.newest, nil }

func (f *fakePlatformEvents) Since(_ context.Context, afterID string, _ int) ([]stream.PlatformEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursors = append(f.cursors, afterID)
	if afterID == f.newest && len(f.events) > 0 {
		out := f.events
		f.events = nil
		return out, nil
	}
	return nil, nil
}

// TestEventsStreamStartsAtTheTailAndServesTheLog: a fresh subscriber is not
// flooded with the retained window; it starts at the current end, and later
// events carry the log's ids so a reconnect can resume.
func TestEventsStreamStartsAtTheTailAndServesTheLog(t *testing.T) {
	cursor := bson.NewObjectID()
	next := bson.NewObjectID()
	fake := &fakePlatformEvents{
		newest: cursor.Hex(),
		events: []stream.PlatformEvent{{ID: next, At: time.Now(), Type: "config_changed", Content: `{"keys":["x"]}`}},
	}
	s := NewServer(nil)
	s.platform = fake
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events/stream", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	var sawEvent bool
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "event: config_changed" {
			sawEvent = true
		}
		if strings.HasPrefix(line, "id: "+next.Hex()) && !sawEvent {
			t.Fatal("the id arrived before the event type")
		}
		if sawEvent && strings.HasPrefix(line, "id: ") {
			break
		}
	}
	if !sawEvent {
		t.Fatal("the stored notification was not delivered")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.cursors) == 0 || fake.cursors[0] != cursor.Hex() {
		t.Fatalf("the stream did not start at the tail: %v", fake.cursors)
	}
}

// TestEventsStreamIgnoresChatID: the conversation guard belongs to the outbound
// route; a stray chat_id on the platform stream must not turn it into a 503.
func TestEventsStreamIgnoresChatID(t *testing.T) {
	s := NewServer(nil) // no database: the hub-only path
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events/stream?chat_id=c1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

type fakeVoiceSessions struct {
	minted map[string]string
}

func (f *fakeVoiceSessions) Mint(_ context.Context, userID string, _ time.Duration) (string, error) {
	id := "id-" + userID
	f.minted[id] = userID
	return id, nil
}

func (f *fakeVoiceSessions) Claim(_ context.Context, id string) (string, bool, error) {
	userID, ok := f.minted[id]
	if ok {
		delete(f.minted, id)
	}
	return userID, ok, nil
}

func TestVoiceSessionsGoThroughTheStore(t *testing.T) {
	fake := &fakeVoiceSessions{minted: map[string]string{}}
	s := NewServer(nil)
	s.voiceStore = fake

	id, err := s.mintVoiceSession(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if userID, ok := s.claimVoiceSession(context.Background(), id); !ok || userID != "user-1" {
		t.Fatalf("claim = %q/%v", userID, ok)
	}
	if _, ok := s.claimVoiceSession(context.Background(), id); ok {
		t.Fatal("the id was accepted twice")
	}
}
