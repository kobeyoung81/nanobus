package server

// The three surfaces that moved in from the console's and the auth service's own
// tables, exercised through the HTTP layer the callers actually use: the shapes
// are the callers', so a field rename here is a breaking change there.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// call runs one request against the REST mux and returns the status and body.
func call(t *testing.T, s *Server, method, path, body string) (int, string) {
	t.Helper()
	var reader *bytes.Reader
	if body == "" {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestRestOAuthLinks_Lifecycle(t *testing.T) {
	s := testServerMongo(t)

	if code, body := call(t, s, http.MethodPost, "/api/v1/users/u-1/oauth-links",
		`{"provider":"ldap","provider_id":"cn=kobeyoung,ou=people"}`); code != http.StatusOK {
		t.Fatalf("link: %d %s", code, body)
	}
	code, body := call(t, s, http.MethodGet, "/api/v1/users/u-1/oauth-links", "")
	if code != http.StatusOK || !strings.Contains(body, "cn=kobeyoung,ou=people") {
		t.Fatalf("list links: %d %s", code, body)
	}

	// The console's roster call: who signs in through the directory.
	code, body = call(t, s, http.MethodGet, "/api/v1/oauth-links?provider=ldap", "")
	if code != http.StatusOK {
		t.Fatalf("roster: %d %s", code, body)
	}
	var roster struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.Unmarshal([]byte(body), &roster); err != nil {
		t.Fatalf("decode roster: %v", err)
	}
	if len(roster.UserIDs) != 1 || roster.UserIDs[0] != "u-1" {
		t.Fatalf("unexpected roster: %+v", roster)
	}

	// The sign-in flow's question: which account is this identity?
	code, body = call(t, s, http.MethodGet, "/api/v1/oauth-links?provider=ldap&provider_id=cn=kobeyoung,ou=people", "")
	if code != http.StatusOK || !strings.Contains(body, "u-1") {
		t.Fatalf("identity lookup: %d %s", code, body)
	}
	if code, body = call(t, s, http.MethodGet, "/api/v1/oauth-links?provider=ldap&provider_id=cn=nobody", ""); code != http.StatusOK ||
		!strings.Contains(body, `"user_ids":[]`) {
		t.Fatalf("an unknown identity should be an empty list: %d %s", code, body)
	}

	if code, body = call(t, s, http.MethodDelete, "/api/v1/users/u-1/oauth-links/ldap", ""); code != http.StatusOK {
		t.Fatalf("unlink: %d %s", code, body)
	}
	if code, body = call(t, s, http.MethodGet, "/api/v1/oauth-links?provider=ldap", ""); code != http.StatusOK || strings.Contains(body, "u-1") {
		t.Fatalf("the roster kept a removed link: %d %s", code, body)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func TestRestRefreshTokens_Lifecycle(t *testing.T) {
	s := testServerMongo(t)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)

	code, body := call(t, s, http.MethodPost, "/api/v1/auth/refresh-tokens",
		`{"token_hash":"hash-1","user_id":"u-1","expires_at":"`+expires+`"}`)
	if code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	code, body = call(t, s, http.MethodGet, "/api/v1/auth/refresh-tokens/hash-1", "")
	if code != http.StatusOK || !strings.Contains(body, "u-1") {
		t.Fatalf("read: %d %s", code, body)
	}
	if code, _ = call(t, s, http.MethodGet, "/api/v1/auth/refresh-tokens/ghost", ""); code != http.StatusNotFound {
		t.Fatalf("an unknown hash should be 404, got %d", code)
	}
	if code, body = call(t, s, http.MethodDelete, "/api/v1/auth/refresh-tokens/hash-1", ""); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, body)
	}
	if code, _ = call(t, s, http.MethodGet, "/api/v1/auth/refresh-tokens/hash-1", ""); code != http.StatusNotFound {
		t.Fatalf("a revoked hash should be 404, got %d", code)
	}
	// Logout revokes everything the account holds.
	if code, body = call(t, s, http.MethodPost, "/api/v1/auth/refresh-tokens",
		`{"token_hash":"hash-2","user_id":"u-2","expires_at":"`+expires+`"}`); code != http.StatusOK {
		t.Fatalf("second save: %d %s", code, body)
	}
	if code, body = call(t, s, http.MethodDelete, "/api/v1/auth/refresh-tokens?user_id=u-2", ""); code != http.StatusOK {
		t.Fatalf("revoke all: %d %s", code, body)
	}
	if code, _ = call(t, s, http.MethodGet, "/api/v1/auth/refresh-tokens/hash-2", ""); code != http.StatusNotFound {
		t.Fatalf("user revocation left the ticket: %d", code)
	}
}

// TestRestUserAccounts_EmailLookupAndRename is the sign-in flow's two calls: find
// the account by the address that was typed, then rename it.
func TestRestUserAccounts_EmailLookupAndRename(t *testing.T) {
	s := testServerMongo(t)

	code, body := call(t, s, http.MethodPost, "/api/v1/users",
		`{"id":"u-1","display_name":"Kobe","email":"kobeyoung81@sina.com","password_hash":"bcrypt-hash"}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d %s", code, body)
	}

	code, body = call(t, s, http.MethodGet, "/api/v1/user-accounts?email=kobeyoung81@sina.com", "")
	if code != http.StatusOK || !strings.Contains(body, `"user_id":"u-1"`) {
		t.Fatalf("lookup: %d %s", code, body)
	}
	if !strings.Contains(body, `"email":"kobeyoung81@sina.com"`) {
		t.Fatalf("the address is missing from the account shape: %s", body)
	}
	if !strings.Contains(body, `"ldap":false`) {
		t.Fatalf("the directory flag is missing from the account shape: %s", body)
	}

	code, body = call(t, s, http.MethodGet, "/api/v1/user-accounts?email=nobody@example.com", "")
	if code != http.StatusOK || !strings.Contains(body, `"users":[]`) {
		t.Fatalf("an unknown address should be an empty list: %d %s", code, body)
	}

	code, body = call(t, s, http.MethodPatch, "/api/v1/user-accounts/u-1", `{"display_name":"Renamed"}`)
	if code != http.StatusOK {
		t.Fatalf("rename: %d %s", code, body)
	}
	if code, body = call(t, s, http.MethodGet, "/api/v1/user-accounts/u-1", ""); !strings.Contains(body, "Renamed") {
		t.Fatalf("rename did not stick: %d %s", code, body)
	}

	// A second account cannot take the same address.
	code, body = call(t, s, http.MethodPost, "/api/v1/users",
		`{"id":"u-2","display_name":"Other","email":"kobeyoung81@sina.com"}`)
	if code != http.StatusConflict {
		t.Fatalf("a duplicate address should be 409, got %d %s", code, body)
	}
}
