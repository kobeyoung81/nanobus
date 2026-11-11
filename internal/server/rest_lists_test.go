package server

// Empty catalogs must reach the console as arrays, not null: the capability page
// read `.length` on a null tool list and blanked the whole console on a fresh
// installation. Both lists it covered are the admin module's now — the bus only
// forwards them — so the wire shape is pinned where the data is served, and what
// is left here is the helper that keeps a nil slice off the wire.

import (
	"encoding/json"
	"testing"
)

func TestNonNilSlice(t *testing.T) {
	empty := nonNilSlice[int](nil)
	if empty == nil {
		t.Fatal("nonNilSlice(nil) returned nil")
	}
	raw, err := json.Marshal(map[string]any{"xs": empty})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(raw) != `{"xs":[]}` {
		t.Fatalf("encoded %s; want {\"xs\":[]}", raw)
	}
	if got := nonNilSlice([]int{1, 2}); len(got) != 2 {
		t.Fatalf("nonNilSlice kept %v; want two elements", got)
	}
}
