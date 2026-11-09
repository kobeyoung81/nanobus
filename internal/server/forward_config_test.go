package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// writeConfigViaBus drives the bus's PUT route and returns the recorder.
func writeConfigViaBus(t *testing.T, s *Server, component, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/config/"+component+"/"+key, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	mux.ServeHTTP(rec, req)
	return rec
}

// The bus no longer writes admin_config: it asks the module that owns the
// collection. This pins the contract — method, path, token, body — because a
// silent change there would turn every console save into a 404 or a 401 on the
// other side, and the bus's own answer would still look fine.
func TestConfigWriteIsForwardedToTheAdminModule(t *testing.T) {
	type captured struct {
		method string
		path   string
		token  string
		body   string
	}
	got := make(chan captured, 1)

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- captured{method: r.Method, path: r.URL.Path, token: r.Header.Get("Authorization"), body: string(body)}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"component_type":"agent","key":"agent.language.default","value":"en"}`))
	}))
	defer admin.Close()

	s := NewServer(storetest.Connect(t), WithAdminURL(admin.URL), WithInternalToken("shared-secret"))

	rec := writeConfigViaBus(t, s, "agent", "agent.language.default",
		`{"value":"en","value_type":"string","description":"language"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	c := <-got
	if c.method != http.MethodPut {
		t.Errorf("method = %s, want PUT", c.method)
	}
	if c.path != "/api/v1/config/agent/agent.language.default" {
		t.Errorf("path = %s", c.path)
	}
	if c.token != "Bearer shared-secret" {
		t.Errorf("token = %q, want the shared internal token", c.token)
	}
	for _, want := range []string{`"value":"en"`, `"value_type":"string"`, `"description":"language"`} {
		if !strings.Contains(c.body, want) {
			t.Errorf("forwarded body = %s, missing %s", c.body, want)
		}
	}
}

// A write must not be reported as successful when the owner refused it.
func TestConfigWriteRelaysAnUpstreamFailure(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error"}`))
	}))
	defer admin.Close()

	s := NewServer(storetest.Connect(t), WithAdminURL(admin.URL), WithInternalToken("t"))
	rec := writeConfigViaBus(t, s, "agent", "k", `{"value":"v"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want the upstream 500 relayed", rec.Code)
	}
}

// Without a configured owner the bus refuses rather than writing the
// collection itself: "save" must never quietly land in the wrong place.
func TestConfigWriteWithoutAnAdminModuleIsRefused(t *testing.T) {
	s := NewServer(storetest.Connect(t))
	rec := writeConfigViaBus(t, s, "agent", "k", `{"value":"v"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
