package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// The bridge exists for one deployment round: containers that still run the
// previous runtime image speak these three messages, and losing them would
// silence a user's history and strand a newly created agent. Each case pins the
// answer shape that older client decodes.

func TestLegacyListAgentsForwardsToThePortal(t *testing.T) {
	var gotPath, gotQuery string
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.Query().Get("user_id")
		_, _ = w.Write([]byte(`{"agents":[{"id":"a1","user_id":"u1","display_name":"A","status":"running"}]}`))
	}))
	defer portal.Close()

	s := NewServer(nil, WithPortalURL(portal.URL, ""))

	msg := nanoprotocol.Message{Type: legacyTypeListAgents, Payload: json.RawMessage(`{"user_id":"u1"}`)}
	resp := s.handleMessage(msg)
	if resp == nil || resp.Type != "agents_listed" {
		t.Fatalf("response = %+v", resp)
	}
	if gotPath != "/internal/v1/agents" || gotQuery != "u1" {
		t.Fatalf("forwarded to %q?user_id=%q", gotPath, gotQuery)
	}
	var out struct {
		Agents []struct {
			ID string `json:"id"`
		} `json:"agents"`
	}
	if err := resp.DecodePayload(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Agents) != 1 || out.Agents[0].ID != "a1" {
		t.Fatalf("agents = %+v", out.Agents)
	}
}

func TestLegacySaveSessionMapsConflictToResync(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		wantResync bool
	}{
		{"accepted", http.StatusOK, false},
		{"resync", http.StatusConflict, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotBody map[string]any
			portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{}`))
			}))
			defer portal.Close()

			s := NewServer(nil, WithPortalURL(portal.URL, ""))
			msg := nanoprotocol.Message{
				Type:    legacyTypeSaveSession,
				Payload: json.RawMessage(`{"session_id":"web:c1","agent_id":"a1","messages":"[]"}`),
			}
			resp := s.handleMessage(msg)
			if resp == nil || resp.Type != "session_saved" {
				t.Fatalf("response = %+v", resp)
			}
			if gotPath != "/internal/v1/sessions" || gotBody["session_id"] != "web:c1" {
				t.Fatalf("forwarded %q body=%+v", gotPath, gotBody)
			}
			var out struct {
				Resync bool `json:"resync"`
			}
			if err := resp.DecodePayload(&out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Resync != tc.wantResync {
				t.Fatalf("resync = %v, want %v", out.Resync, tc.wantResync)
			}
		})
	}
}

func TestLegacyUpdateAgentStatusForwards(t *testing.T) {
	var gotPath string
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"a1","status":"running"}`))
	}))
	defer portal.Close()

	s := NewServer(nil, WithPortalURL(portal.URL, ""))
	msg := nanoprotocol.Message{
		Type:    legacyTypeUpdateAgentStatus,
		Payload: json.RawMessage(`{"user_id":"u1","agent_id":"a1","status":"running"}`),
	}
	resp := s.handleMessage(msg)
	if resp == nil || resp.Type != "ok" {
		t.Fatalf("response = %+v", resp)
	}
	if gotPath != "/internal/v1/agents/a1/status" {
		t.Fatalf("forwarded to %q", gotPath)
	}
}
