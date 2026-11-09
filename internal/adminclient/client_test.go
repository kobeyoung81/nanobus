package adminclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The bus validates a saved policy against the read-only keys; if this client
// silently returned an empty catalog, a policy that unlocks a read-only tool
// would look valid. A refusal must therefore be an error, not "no keys".
func TestPermissionKeysRejectsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "t").PermissionKeys(context.Background()); err == nil {
		t.Fatal("a 503 was accepted as an empty catalog")
	}
}

func TestPermissionKeysPresentsTheToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{{"id": "readonly_bash", "read_only": true, "default_action": "allow"}},
		})
	}))
	defer srv.Close()

	keys, err := New(srv.URL, "s3cret").PermissionKeys(context.Background())
	if err != nil {
		t.Fatalf("PermissionKeys: %v", err)
	}
	if got != "Bearer s3cret" {
		t.Fatalf("Authorization = %q", got)
	}
	if len(keys) != 1 || keys[0].ID != "readonly_bash" || !keys[0].ReadOnly {
		t.Fatalf("keys = %+v", keys)
	}
}
