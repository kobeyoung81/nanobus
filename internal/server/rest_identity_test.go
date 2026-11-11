package server

// The three surfaces that moved in from the console's and the auth service's own
// tables, exercised through the HTTP layer the callers actually use: the shapes
// are the callers', so a field rename here is a breaking change there.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
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

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

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
