package server

// The console action that gives a platform model to every built-in agent type.
// The store logic has its own test; this pins the route, the response the
// console reads, and the 404/400 answers.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanoprotocol"
)

func seedAgentTypeDoc(t *testing.T, s *Server, id string, seeded bool) {
	t.Helper()
	if _, err := s.store.DB().Collection("agent_types").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id}, {Key: "name", Value: id}, {Key: "seeded", Value: seeded},
		{Key: "strategy", Value: "react"}, {Key: "context", Value: "windowed"},
		{Key: "output", Value: "stream"}, {Key: "session_binding", Value: "shared"},
	}); err != nil {
		t.Fatalf("seed type %s: %v", id, err)
	}
}

func TestRestApplyModelToBuiltInTypes(t *testing.T) {
	s := testServerMongo(t)
	providerID, err := s.store.CreateModelProvider(nanoprotocol.ModelProviderDef{
		Scope: "platform", Name: "gateway", BaseURL: "http://example.test",
	})
	if err != nil {
		t.Fatalf("CreateModelProvider: %v", err)
	}
	modelID, err := s.store.CreateModel(nanoprotocol.ModelDef{
		ProviderID: providerID, Scope: "platform", Name: "pro", Model: "pro",
	})
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	// `seeded` belongs to the catalog seeder; SaveAgentType never writes it, so a
	// fixture that needs a built-in type inserts the document itself.
	seedAgentTypeDoc(t, s, "build", true)
	seedAgentTypeDoc(t, s, "custom", false)

	path := fmt.Sprintf("/api/v1/models/models/%d/apply-to-agent-types", modelID)
	code, body := call(t, s, http.MethodPost, path, "")
	if code != http.StatusOK {
		t.Fatalf("apply: %d %s", code, body)
	}
	var result struct {
		ModelID  int64    `json:"model_id"`
		Applied  []string `json:"applied"`
		Already  []string `json:"already"`
		Defaults []string `json:"defaults"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.ModelID != modelID || len(result.Applied) != 1 || result.Applied[0] != "build" {
		t.Fatalf("result = %+v; want model %d applied to [build]", result, modelID)
	}
	if len(result.Defaults) != 1 || result.Defaults[0] != "build" {
		t.Fatalf("defaults = %v; want [build]", result.Defaults)
	}

	// Unknown models are 404; the route must not turn them into an empty success.
	if code, _ := call(t, s, http.MethodPost, "/api/v1/models/models/99999/apply-to-agent-types", ""); code != http.StatusNotFound {
		t.Fatalf("unknown model: got %d; want 404", code)
	}
}

func TestRestApplyModelToBuiltInTypes_RejectsUserModels(t *testing.T) {
	s := testServerMongo(t)
	providerID, err := s.store.CreateModelProvider(nanoprotocol.ModelProviderDef{
		Scope: "user", OwnerUserID: "u-1", Name: "mine", BaseURL: "http://example.test",
	})
	if err != nil {
		t.Fatalf("CreateModelProvider: %v", err)
	}
	modelID, err := s.store.CreateModel(nanoprotocol.ModelDef{
		ProviderID: providerID, Scope: "user", OwnerUserID: "u-1", Name: "mine", Model: "mine",
	})
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	path := fmt.Sprintf("/api/v1/models/models/%d/apply-to-agent-types", modelID)
	if code, body := call(t, s, http.MethodPost, path, ""); code != http.StatusBadRequest {
		t.Fatalf("user model: got %d %s; want 400", code, body)
	}
}
