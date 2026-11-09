package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// The language setting has two levels: the platform default and the account's
// own choice. The console shows the effective value and where it came from, and
// the runtime receives only the account-level half (the platform default travels
// as a config item).

func TestRestPreferences(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUser(t, srv.store, "u-1", "free", 0)
	if err := setConfigValue(t, srv.store, "agent", "agent.language.default", "zh"); err != nil {
		t.Fatalf("seed platform default: %v", err)
	}

	get := func() preferencesResponse {
		t.Helper()
		rec := accountRequest(t, srv, http.MethodGet, "/api/v1/user/preferences?user_id=u-1", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("get preferences = %d: %s", rec.Code, rec.Body.String())
		}
		var body preferencesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode preferences: %v", err)
		}
		return body
	}

	// No account-level choice: the platform default is the effective language.
	if got := get(); got.Language != "zh" || got.Source != "platform" {
		t.Fatalf("initial preferences = %+v, want zh/platform", got)
	}

	// An account choice wins, and a regional tag is stored as its primary subtag.
	rec := accountRequest(t, srv, http.MethodPut, "/api/v1/user/preferences", map[string]any{
		"user_id": "u-1", "language": "EN-gb",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("put preferences = %d: %s", rec.Code, rec.Body.String())
	}
	if got := get(); got.Language != "en" || got.Source != "user" {
		t.Fatalf("after choosing = %+v, want en/user", got)
	}

	// Clearing it falls back to the platform default again.
	rec = accountRequest(t, srv, http.MethodPut, "/api/v1/user/preferences", map[string]any{
		"user_id": "u-1", "language": "",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear preferences = %d: %s", rec.Code, rec.Body.String())
	}
	if got := get(); got.Language != "zh" || got.Source != "platform" {
		t.Fatalf("after clearing = %+v, want zh/platform", got)
	}

	// An unknown tag is refused rather than stored.
	rec = accountRequest(t, srv, http.MethodPut, "/api/v1/user/preferences", map[string]any{
		"user_id": "u-1", "language": "klingon",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported language = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestWithUserLanguage covers what the runtime actually receives: the account's
// choice, unless the agent carries a setting of its own. The platform default is
// not folded in — it travels as a config item.
func TestWithUserLanguage(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUser(t, srv.store, "u-1", "free", 0)
	if err := srv.store.SetUserPreferredLanguage("u-1", "ja"); err != nil {
		t.Fatalf("set preference: %v", err)
	}

	got := srv.withUserLanguage(nil, "u-1")
	if got["language"] != "ja" {
		t.Fatalf("folded settings = %+v, want language=ja", got)
	}

	own := map[string]string{"language": "en"}
	if got := srv.withUserLanguage(own, "u-1"); got["language"] != "en" {
		t.Fatalf("agent setting was overwritten: %+v", got)
	}

	storetest.SeedUser(t, srv.store, "u-2", "free", 2)
	if got := srv.withUserLanguage(nil, "u-2"); got["language"] != "" {
		t.Fatalf("an account with no choice got a language: %+v", got)
	}
}
