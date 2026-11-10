package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// TestHandleUpdateAgentStatusForwardsToPortal pins the one write the supervisor
// still sends through this service: the agent rows belong to the portal, so the
// bus must forward the report instead of writing the document itself.
func TestHandleUpdateAgentStatusForwardsToPortal(t *testing.T) {
	var gotPath, gotStatus, gotToken string
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("Authorization")
		var body struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotStatus = body.Status
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"agent-1","status":"running"}`))
	}))
	defer portal.Close()

	s := NewServer(nil, WithPortalURL(portal.URL, "s3cret"))

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeUpdateAgentStatus, nanoprotocol.UpdateAgentStatusRequest{
		UserID:  "user-1",
		AgentID: "agent-1",
		Status:  "running",
	})
	resp := s.handleUpdateAgentStatus(msg)
	if resp == nil || resp.Type == nanoprotocol.TypeError {
		t.Fatalf("expected a non-error response, got %+v", resp)
	}
	if gotPath != "/internal/v1/agents/agent-1/status" {
		t.Fatalf("portal path = %q", gotPath)
	}
	if gotStatus != "running" {
		t.Fatalf("forwarded status = %q", gotStatus)
	}
	if gotToken != "Bearer s3cret" {
		t.Fatalf("forwarded token = %q", gotToken)
	}
}

// Without a configured portal the report must fail loudly: silently accepting it
// would leave a dead agent looking alive forever.
func TestHandleUpdateAgentStatusWithoutPortalFails(t *testing.T) {
	s := NewServer(nil)

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeUpdateAgentStatus, nanoprotocol.UpdateAgentStatusRequest{
		AgentID: "agent-1",
		Status:  "running",
	})
	resp := s.handleUpdateAgentStatus(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeError {
		t.Fatalf("expected an error response, got %+v", resp)
	}
}

func TestHandleUpdateAgentStatusRequiresFields(t *testing.T) {
	s := NewServer(nil)

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeUpdateAgentStatus, nanoprotocol.UpdateAgentStatusRequest{AgentID: "agent-1"})
	resp := s.handleUpdateAgentStatus(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeError {
		t.Fatalf("expected an error response, got %+v", resp)
	}
}
