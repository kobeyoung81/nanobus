package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
)

// The identity links are the auth service's now; these endpoints keep the console's
// shapes and forward. What is asserted is that the shape did not change and that
// the module's answers come back as themselves.

func TestRestListOAuthLinksForwards(t *testing.T) {
	var got authCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = authCall{method: r.Method, path: r.URL.RequestURI(), auth: r.Header.Get("Authorization")}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"links":[{"user_id":"u-1","provider":"ldap","provider_id":"cn=ada"}]}`))
	}))
	defer srv.Close()
	s := wireServer(t)
	s.auth = authclient.New(srv.URL, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u-1/oauth-links", nil)
	req.SetPathValue("userID", "u-1")
	rec := httptest.NewRecorder()
	s.restListOAuthLinks(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Links []map[string]any `json:"links"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Links) != 1 || payload.Links[0]["provider_id"] != "cn=ada" {
		t.Fatalf("the console's shape changed: %+v", payload.Links)
	}
	if got.path != "/internal/v1/users/u-1/oauth-links" || got.auth != "Bearer test-token" {
		t.Fatalf("call did not travel: %+v", got)
	}
}

// TestRestListOAuthLinksByProviderDedupes: the console asks for a set of accounts,
// while the module answers with links; one account linked twice appears once.
func TestRestListOAuthLinksByProviderDedupes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"links":[
			{"user_id":"u-1","provider":"ldap","provider_id":"cn=ada"},
			{"user_id":"u-1","provider":"ldap","provider_id":"cn=ada2"},
			{"user_id":"u-2","provider":"ldap","provider_id":"cn=bob"}]}`))
	}))
	defer srv.Close()
	s := wireServer(t)
	s.auth = authclient.New(srv.URL, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/oauth-links?provider=ldap", nil)
	rec := httptest.NewRecorder()
	s.restListOAuthLinksByProvider(rec, req)

	var payload struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.UserIDs) != 2 || payload.UserIDs[0] != "u-1" || payload.UserIDs[1] != "u-2" {
		t.Fatalf("the set changed: %+v", payload.UserIDs)
	}
}

// TestRestLinkOAuthForwardsTheIdentity.
func TestRestLinkOAuthForwardsTheIdentity(t *testing.T) {
	var got authCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 256)
		n, _ := r.Body.Read(body)
		got = authCall{method: r.Method, path: r.URL.RequestURI(), body: string(body[:n])}
		_, _ = w.Write([]byte(`{"status":"linked"}`))
	}))
	defer srv.Close()
	s := wireServer(t)
	s.auth = authclient.New(srv.URL, "test-token")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/u-1/oauth-links",
		strings.NewReader(`{"provider":"ldap","provider_id":"cn=ada"}`))
	req.SetPathValue("userID", "u-1")
	rec := httptest.NewRecorder()
	s.restLinkOAuth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if got.method != http.MethodPost || got.path != "/internal/v1/users/u-1/oauth-links/ldap" {
		t.Fatalf("call did not travel: %+v", got)
	}
	if !strings.Contains(got.body, `"cn=ada"`) {
		t.Fatalf("the identity did not travel: %s", got.body)
	}
}

// TestRestOAuthLinksReportAnUnavailableModule.
func TestRestOAuthLinksReportAnUnavailableModule(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := wireServer(t)
	s.auth = authclient.New(srv.URL, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u-1/oauth-links", nil)
	req.SetPathValue("userID", "u-1")
	rec := httptest.NewRecorder()
	s.restListOAuthLinks(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}
