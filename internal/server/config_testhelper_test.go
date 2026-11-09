package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/adminclient"
	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// adminConfigStub is a fake admin module. The bus reads configuration from
// admin, not from a collection it owns, so tests that need configuration serve
// it the way the deployment does: over HTTP.
type adminConfigStub struct {
	mu    sync.Mutex
	items map[string][]store.ConfigItem
	srv   *httptest.Server
}

var adminStubs sync.Map // *Server → *adminConfigStub

// adminStubFor attaches (once) a fake admin module to a server and returns it.
func adminStubFor(t *testing.T, s *Server) *adminConfigStub {
	t.Helper()
	if existing, ok := adminStubs.Load(s); ok {
		return existing.(*adminConfigStub)
	}

	stub := &adminConfigStub{items: map[string][]store.ConfigItem{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/config/{component}", func(w http.ResponseWriter, r *http.Request) {
		component := r.PathValue("component")
		stub.mu.Lock()
		items := append([]store.ConfigItem(nil), stub.items[component]...)
		stub.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"component_type": component,
			"items":          items,
		})
	})
	stub.srv = httptest.NewServer(mux)
	t.Cleanup(stub.srv.Close)

	s.admin = adminclient.New(stub.srv.URL, "test-token")
	adminStubs.Store(s, stub)
	return stub
}

// seedConfig serves configuration to the server through the fake admin module,
// mirroring production: the bus no longer writes (or reads) this collection.
func seedConfig(t *testing.T, s *Server, component string, items []store.ConfigItem) error {
	t.Helper()
	stub := adminStubFor(t, s)
	stub.mu.Lock()
	stub.items[component] = append(stub.items[component], items...)
	stub.mu.Unlock()
	return nil
}

// setConfigValue overwrites one item in the fake admin module, matching what the
// admin service's upsert does for an existing key.
func setConfigValue(t *testing.T, s *Server, component, key, value string) error {
	t.Helper()
	stub := adminStubFor(t, s)
	stub.mu.Lock()
	defer stub.mu.Unlock()
	for i, item := range stub.items[component] {
		if item.Key == key {
			stub.items[component][i].Value = value
			return nil
		}
	}
	stub.items[component] = append(stub.items[component], store.ConfigItem{Key: key, Value: value})
	return nil
}
