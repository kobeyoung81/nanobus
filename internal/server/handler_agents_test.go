package server

import (
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func TestHandleCreateAgent(t *testing.T) {
	// The whole path — the tier, the container request, the agent document — is in
	// MongoDB now, so the user is seeded there and nothing needs mocking.
	s := testServerMongo(t)
	storetest.SeedUser(t, s.store, "user-1", "pro", 1)

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeCreateAgent, nanoprotocol.CreateAgentRequest{
		UserID:      "user-1",
		ID:          "agent-1",
		DisplayName: "Research",
		Description: "desc",
		Model:       "gpt-4o",
	})
	resp := s.handleCreateAgent(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeAgentCreated {
		t.Fatalf("expected agent_created response, got %+v", resp)
	}
}

func TestHandleCreateAgent_RequiresUserID(t *testing.T) {
	s := NewServer(storetest.Connect(t))

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeCreateAgent, nanoprotocol.CreateAgentRequest{ID: "agent-1"})
	resp := s.handleCreateAgent(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeError {
		t.Fatalf("expected error response, got %+v", resp)
	}
}

func TestHandleUpdateAgent(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "active")

	displayName := "Research v2"
	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeUpdateAgent, nanoprotocol.UpdateAgentRequest{
		ID:          "agent-1",
		DisplayName: &displayName,
	})
	resp := s.handleUpdateAgent(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeAgentUpdated {
		t.Fatalf("expected agent_updated response, got %+v", resp)
	}
}

// TestHandleUpdateAgent_NotFound pins the answer for an agent that is not there.
// It used to arrange that by mocking an empty SELECT against `agents`; the agents
// domain is in MongoDB now, so an empty store is the honest arrangement.
func TestHandleUpdateAgent_NotFound(t *testing.T) {
	s := testServerMongo(t)

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeUpdateAgent, nanoprotocol.UpdateAgentRequest{ID: "missing"})
	resp := s.handleUpdateAgent(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeError {
		t.Fatalf("expected error response, got %+v", resp)
	}
}

func TestHandleDeleteAgent(t *testing.T) {
	s := testServerMongo(t)
	seedAgent(t, s, "agent-1", "user-1", "running")

	msg, _ := nanoprotocol.NewMessage(nanoprotocol.TypeDeleteAgent, nanoprotocol.DeleteAgentRequest{ID: "agent-1"})
	resp := s.handleDeleteAgent(msg)
	if resp == nil || resp.Type != nanoprotocol.TypeAgentDeleted {
		t.Fatalf("expected agent_deleted response, got %+v", resp)
	}
}
