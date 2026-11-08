package server

import (
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/stream"
)

func TestSSEHub_BroadcastDelivery(t *testing.T) {
	h := newSSEHub()
	sub := h.subscribe("web")

	ev := stream.ChunkEvent{Type: "chunk", Content: "hello"}
	h.broadcast("web", ev)

	select {
	case got := <-sub.ch:
		if got.Content != "hello" {
			t.Fatalf("content = %q, want %q", got.Content, "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broadcast event")
	}
}

func TestSSEHub_Unsubscribe(t *testing.T) {
	h := newSSEHub()
	sub := h.subscribe("web")
	h.unsubscribe("web", sub)

	// No panic and no delivery after unsubscribe.
	h.broadcast("web", stream.ChunkEvent{Type: "chunk"})
	select {
	case <-sub.ch:
		t.Fatal("received event after unsubscribe")
	case <-time.After(100 * time.Millisecond):
	}
}
