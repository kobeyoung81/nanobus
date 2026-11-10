package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func TestHandleValidateToken_Valid(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/v1/userinfo" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			http.Error(w, "missing bearer", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "user-1", "role": "operator"})
	}))
	defer auth.Close()

	s := NewServer(storetest.Connect(t), WithNanoauthURL(auth.URL))

	msg, err := nanoprotocol.NewMessage(nanoprotocol.TypeValidateToken, nanoprotocol.ValidateTokenRequest{Token: "tok-123"})
	if err != nil {
		t.Fatal(err)
	}

	resp := s.handleValidateToken(msg)
	if resp == nil {
		t.Fatal("expected a response")
	}
	if resp.Type != nanoprotocol.TypeTokenValid {
		t.Fatalf("expected type %q, got %q", nanoprotocol.TypeTokenValid, resp.Type)
	}
	var out nanoprotocol.ValidateTokenResponse
	if err := resp.DecodePayload(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Valid || out.UserID != "user-1" || out.Role != "operator" {
		t.Fatalf("unexpected response: %+v", out)
	}
}

func TestHandleValidateToken_RejectsInvalidToken(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
	}))
	defer auth.Close()

	s := NewServer(storetest.Connect(t), WithNanoauthURL(auth.URL))

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeValidateToken, nanoprotocol.ValidateTokenRequest{Token: "bad"})
	resp := s.handleValidateToken(msg)
	var out nanoprotocol.ValidateTokenResponse
	if err := resp.DecodePayload(&out); err != nil {
		t.Fatal(err)
	}
	if out.Valid {
		t.Fatal("expected token to be rejected")
	}
}

func TestHandleValidateToken_MissingAuthConfig(t *testing.T) {
	s := NewServer(storetest.Connect(t))

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeValidateToken, nanoprotocol.ValidateTokenRequest{Token: "tok"})
	resp := s.handleValidateToken(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeError {
		t.Fatalf("expected error response, got %+v", resp)
	}
}
