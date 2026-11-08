package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/stream"
)

func TestSSEStream_ChunkDelivery(t *testing.T) {
	s := wireServer(t)

	ts := httptest.NewServer(restMux(t, s))
	defer ts.Close()

	// Subscribe to the SSE stream first.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/outbound/stream?channel=web", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	streamResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer streamResp.Body.Close()
	t.Logf("stream opened, status=%d", streamResp.StatusCode)

	if streamResp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", streamResp.StatusCode)
	}
	if ct := streamResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// Post a chunk and a complete.
	chunkBody := `{"message_id":"m1","seq":1,"content":"Hi ","agent_id":"assistant","user_id":"u1","channel":"web","chat_id":"c1"}`
	resp, err := http.Post(ts.URL+"/api/v1/outbound/chunk", "application/json", bytes.NewReader([]byte(chunkBody)))
	if err != nil {
		t.Fatalf("post chunk: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("chunk status = %d, want 202", resp.StatusCode)
	}

	completeBody := `{"message_id":"m1","full_content":"Hi there!","agent_id":"assistant","user_id":"u1","channel":"web","chat_id":"c1"}`
	resp, err = http.Post(ts.URL+"/api/v1/outbound/complete", "application/json", bytes.NewReader([]byte(completeBody)))
	if err != nil {
		t.Fatalf("post complete: %v", err)
	}
	resp.Body.Close()

	// Read SSE events from the stream.
	reader := bufio.NewReader(streamResp.Body)
	lineCh := make(chan string, 8)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				lineCh <- "ERR:" + err.Error()
				return
			}
			lineCh <- line
		}
	}()

	var sawChunk, sawComplete bool
	for !sawChunk || !sawComplete {
		var eventType string
		var data string
	inner:
		for {
			select {
			case <-time.After(5 * time.Second):
				t.Fatalf("timeout reading stream (chunk=%v complete=%v)", sawChunk, sawComplete)
			case line := <-lineCh:
				t.Logf("line: %q", line)
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "event:") {
					eventType = strings.TrimSpace(line[len("event:"):])
				} else if strings.HasPrefix(line, "id:") {
					// ignore
				} else if strings.HasPrefix(line, "data:") {
					data = strings.TrimSpace(line[len("data:"):])
				} else if line == "" && data != "" {
					break inner
				}
			}
		}

		var payload map[string]any
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			t.Fatalf("decode event data: %v", err)
		}

		switch eventType {
		case "chunk":
			if payload["content"] != "Hi " {
				t.Errorf("chunk content = %v, want 'Hi '", payload["content"])
			}
			sawChunk = true
		case "complete":
			if payload["full_content"] != "Hi there!" {
				t.Errorf("complete content = %v, want 'Hi there!'", payload["full_content"])
			}
			sawComplete = true
		}
	}
}

// panickingFlusher simulates a response whose connection was torn down by the
// client: Flush panics the way net/http does in that race.
type panickingFlusher struct{ w http.ResponseWriter }

func (p *panickingFlusher) Header() http.Header         { return p.w.Header() }
func (p *panickingFlusher) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *panickingFlusher) WriteHeader(code int)        { p.w.WriteHeader(code) }
func (p *panickingFlusher) Flush() {
	panic("runtime error: invalid memory address or nil pointer dereference")
}

// TestSSEWriter_CommentRecoversFlushPanic verifies that a panic inside Flush is
// recovered into an error instead of crashing the process (regression test for
// the keepSSEAlive SIGSEGV that took down nanobus when an SSE client
// disconnected).
func TestSSEWriter_CommentRecoversFlushPanic(t *testing.T) {
	var buf bytes.Buffer
	rec := httptest.NewRecorder()
	rec.Body = &buf

	sw := &sseWriter{w: rec, f: &panickingFlusher{w: rec}}

	err := sw.comment()
	if err == nil {
		t.Fatal("expected comment() to return an error when Flush panics")
	}
	if !strings.Contains(err.Error(), "sse flush panic") {
		t.Errorf("unexpected error: %v", err)
	}

	// keepSSEAlive must exit without panicking when the connection dies.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	keepSSEAlive(ctx, sw) // must not panic
}

// TestSSEWriter_CommentHappyPath verifies normal keep-alive comments work.
func TestSSEWriter_CommentHappyPath(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &sseWriter{w: rec, f: rec}
	if err := sw.comment(); err != nil {
		t.Fatalf("comment(): %v", err)
	}
	if got := rec.Body.String(); !strings.Contains(got, ": keepalive") {
		t.Errorf("expected keepalive comment, got %q", got)
	}
}

// TestSSEStream_ResumePointFromAnotherProcess is the failure the cutover found:
// the SSE IDs are a per-process counter, but Last-Event-ID outlives the process.
//
// A subscriber that reconnects after the bus was replaced asks to resume from an
// ID the previous process handed out (say 20000) while the new one is at 1. Every
// live event then fails the `ID <= lastSent` guard, and the subscriber — a
// gateway, a browser tab — looks connected and receives nothing, forever, until
// enough events have been produced to climb past the stale number. Chunks stop
// arriving live and only a full re-read shows them.
func TestSSEStream_ResumePointFromAnotherProcess(t *testing.T) {
	s := wireServer(t)

	ts := httptest.NewServer(restMux(t, s))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/outbound/stream?channel=web", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Last-Event-ID", "20000") // from the process before this one
	streamResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer streamResp.Body.Close()

	reader := bufio.NewReader(streamResp.Body)
	lineCh := make(chan string, 8)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				lineCh <- "ERR:" + err.Error()
				return
			}
			lineCh <- line
		}
	}()

	// Give the handler a moment to subscribe, then produce one event.
	time.Sleep(100 * time.Millisecond)
	chunkBody := `{"message_id":"m1","seq":1,"content":"after restart","agent_id":"assistant","user_id":"u1","channel":"web","chat_id":"c1"}`
	resp, err := http.Post(ts.URL+"/api/v1/outbound/chunk", "application/json", bytes.NewReader([]byte(chunkBody)))
	if err != nil {
		t.Fatalf("post chunk: %v", err)
	}
	resp.Body.Close()

	sawChunk := false
	deadline := time.After(5 * time.Second)
	for !sawChunk {
		select {
		case <-deadline:
			t.Fatal("the stream went silent: a resume point from a previous process must not mute it")
		case line := <-lineCh:
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(line[len("data:"):])), &payload); err != nil {
				t.Fatalf("decode event data: %v", err)
			}
			if payload["content"] == "after restart" {
				sawChunk = true
			}
		}
	}
}

// TestRingBuffer_Newest pins the value the SSE handler compares a resume point
// against: the last ID handed out, and 0 before anything was appended.
func TestRingBuffer_Newest(t *testing.T) {
	r := stream.NewRingBuffer(0)
	if got := r.Newest(); got != 0 {
		t.Fatalf("Newest() = %d on an empty ring, want 0", got)
	}
	if id := r.Append(stream.ChunkEvent{Type: "chunk"}); id != 1 {
		t.Fatalf("first id = %d, want 1", id)
	}
	if got := r.Newest(); got != 1 {
		t.Fatalf("Newest() = %d after one append, want 1", got)
	}
	r.Append(stream.ChunkEvent{Type: "chunk"})
	if got := r.Newest(); got != 2 {
		t.Fatalf("Newest() = %d after two appends, want 2", got)
	}
}
