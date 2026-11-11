package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The per-user capability secrets are identity's; these three endpoints keep the
// console's shape (masked values, never plaintext) and forward the reads and
// writes.

func TestRestUserSecretsForwardAndMask(t *testing.T) {
	var calls []authCall
	s := wireServer(t)
	s.auth = authStub(t, &calls, http.StatusOK, `{"secrets":{"TOKEN":"sk-1","OTHER":"sk-2"}}`)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user/secrets/mcp/mcp:github?user_id=u-1", nil)
	req.SetPathValue("kind", "mcp")
	req.SetPathValue("ref", "mcp:github")
	rec := httptest.NewRecorder()
	s.restListUserSecrets(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	var payload struct {
		Secrets map[string]string `json:"secrets"`
		Count   int               `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Count != 2 || payload.Secrets["TOKEN"] != maskValue || payload.Secrets["OTHER"] != maskValue {
		t.Fatalf("the values were not masked: %+v", payload)
	}
	// The plaintext must not appear anywhere in what the browser receives.
	if strings.Contains(rec.Body.String(), "sk-1") {
		t.Fatalf("a secret value leaked to the browser: %s", rec.Body.String())
	}
	if len(calls) != 1 || calls[0].path != "/internal/v1/users/u-1/secrets/mcp/mcp:github" {
		t.Fatalf("call did not travel: %+v", calls)
	}
}

func TestRestSetUserSecretForwardsTheValue(t *testing.T) {
	var calls []authCall
	s := wireServer(t)
	s.auth = authStub(t, &calls, http.StatusOK, `{"status":"saved"}`)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/user/secrets/mcp/mcp:github?user_id=u-1",
		strings.NewReader(`{"key":"TOKEN","value":"sk-1"}`))
	req.SetPathValue("kind", "mcp")
	req.SetPathValue("ref", "mcp:github")
	rec := httptest.NewRecorder()
	s.restSetUserSecret(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 || calls[0].method != http.MethodPut {
		t.Fatalf("call did not travel: %+v", calls)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(calls[0].body), &body); err != nil {
		t.Fatalf("decode forwarded body: %v", err)
	}
	if body["key"] != "TOKEN" || body["value"] != "sk-1" {
		t.Fatalf("the value did not travel: %+v", body)
	}
}

// TestRestUserSecretRejectsABadKind: the accepted kinds are the console's
// contract, and they are checked before the module is troubled.
func TestRestUserSecretRejectsABadKind(t *testing.T) {
	var calls []authCall
	s := wireServer(t)
	s.auth = authStub(t, &calls, http.StatusOK, `{"status":"saved"}`)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/user/secrets/banana/x?user_id=u-1",
		strings.NewReader(`{"key":"TOKEN","value":"sk-1"}`))
	req.SetPathValue("kind", "banana")
	req.SetPathValue("ref", "x")
	rec := httptest.NewRecorder()
	s.restSetUserSecret(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(calls) != 0 {
		t.Fatalf("a refused kind was forwarded: %+v", calls)
	}
}

func TestRestDeleteUserSecretForwardsTheKey(t *testing.T) {
	var calls []authCall
	s := wireServer(t)
	s.auth = authStub(t, &calls, http.StatusOK, `{"status":"deleted"}`)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/user/secrets/mcp/mcp:github?user_id=u-1&key=TOKEN", nil)
	req.SetPathValue("kind", "mcp")
	req.SetPathValue("ref", "mcp:github")
	rec := httptest.NewRecorder()
	s.restDeleteUserSecret(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body.String())
	}
	if len(calls) != 1 || calls[0].method != http.MethodDelete || calls[0].path != "/internal/v1/users/u-1/secrets/mcp/mcp:github?key=TOKEN" {
		t.Fatalf("call did not travel: %+v", calls)
	}
}
