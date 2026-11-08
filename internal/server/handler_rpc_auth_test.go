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

func TestHandleListAgents(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeListAgents, nanoprotocol.ListAgentsRequest{UserID: "user-1"})
	resp := s.handleListAgents(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeAgentsListed {
		t.Fatalf("expected agents_listed response, got %+v", resp)
	}
	var out nanoprotocol.ListAgentsResponse
	if err := resp.DecodePayload(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Agents) != 1 || out.Agents[0].ID != "agent-1" || out.Agents[0].UserID != "user-1" {
		t.Fatalf("unexpected agents: %+v", out.Agents)
	}
	// The per-mode model/thinking now ride on the snapshot's Agents list (each
	// AgentTypeDef.Override), not on AgentInfo.
	if out.Agents[0].MaxIterations != 10 {
		t.Fatalf("MaxIterations = %d, want 10", out.Agents[0].MaxIterations)
	}
}

func TestHandleListAgents_RequiresUserID(t *testing.T) {
	s := NewServer(storetest.Connect(t))

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeListAgents, nanoprotocol.ListAgentsRequest{})
	resp := s.handleListAgents(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeError {
		t.Fatalf("expected error response, got %+v", resp)
	}
}
