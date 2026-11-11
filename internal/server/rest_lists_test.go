package server

// Empty catalogs must reach the console as arrays, not null. The capability
// page read `.length` on a null tool list and blanked the whole admin console on
// a fresh installation — the bus's own page, so the fix is pinned at the wire.

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestRestListBuiltin_EmptyCatalogIsAnArray(t *testing.T) {
	s := testServerMongo(t)

	code, body := call(t, s, http.MethodGet, "/api/v1/capabilities/builtin", "")
	if code != http.StatusOK {
		t.Fatalf("list builtin: %d %s", code, body)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(payload["tools"]) != "[]" {
		t.Fatalf("tools = %s; want []", payload["tools"])
	}
}

func TestRestListAlertRules_EmptyIsAnArray(t *testing.T) {
	s := testServerMongo(t)

	// The audit trail is the admin module's now (this service forwards it), so
	// only the rules are still served from here.
	for _, tc := range []struct{ path, field string }{
		{"/api/v1/alert-rules", "rules"},
	} {
		code, body := call(t, s, http.MethodGet, tc.path, "")
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.path, code, body)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &payload); err != nil {
			t.Fatalf("decode %s: %v", tc.path, err)
		}
		if string(payload[tc.field]) != "[]" {
			t.Fatalf("%s: %s = %s; want []", tc.path, tc.field, payload[tc.field])
		}
	}
}

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
