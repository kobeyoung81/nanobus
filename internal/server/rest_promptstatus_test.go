package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/agenttypes"
	"gitee.com/kobeyoung81/nanobus/internal/promptbundle"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// agentTypeWithoutBundle is an admin-created type: no bundle entry, so the page
// must call it `none` rather than `empty`.
func agentTypeWithoutBundle() nanoprotocol.AgentTypeDef {
	return nanoprotocol.AgentTypeDef{
		ID: "custom_type", Name: "Custom", Strategy: nanoprotocol.StrategyReact,
		Context: nanoprotocol.ContextWindowed, Output: nanoprotocol.OutputStream,
		SessionBinding: nanoprotocol.SessionBindingShared,
	}
}

// The admin page can show the prompt text but not where it came from: only the
// bundle in this binary knows whether the stored copy is the bundled one, an
// older seed, or an edit. These two endpoints answer that and restore one type.

func TestRestAgentTypePromptStatusAndReset(t *testing.T) {
	srv := testServerMongo(t)
	if _, err := srv.store.SeedAgentTypes(agenttypes.Definitions()); err != nil {
		t.Fatalf("seed agent types: %v", err)
	}
	bundled, err := promptbundle.Load()
	if err != nil {
		t.Fatalf("load bundle: %v", err)
	}
	if _, _, err := srv.store.SeedPlatformPrompts(bundled, false); err != nil {
		t.Fatalf("seed prompts: %v", err)
	}
	var explore promptbundle.BundledPrompt
	for _, p := range bundled {
		if p.ID == "role_explore" {
			explore = p
		}
	}
	if explore.Text == "" {
		t.Fatal("the bundle has no role_explore prompt")
	}

	status := func() map[string]agentTypePromptStatus {
		t.Helper()
		rec := accountRequest(t, srv, http.MethodGet, "/api/v1/agent-types/prompt-status", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status list = %d: %s", rec.Code, rec.Body.String())
		}
		var body agentTypePromptStatusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode status list: %v", err)
		}
		out := map[string]agentTypePromptStatus{}
		for _, item := range body.Items {
			out[item.ID] = item
		}
		return out
	}

	// A freshly seeded store: every bundled type is exactly the bundle.
	sources := status()
	if got := sources["role_explore"]; got.Source != string(promptbundle.StatusBundled) ||
		got.BundledVersion != explore.Version || got.StoredVersion != explore.Version {
		t.Fatalf("role_explore status = %+v, want bundled v%d", got, explore.Version)
	}

	// An admin edit shows as such.
	def, err := srv.store.GetAgentType("role_explore")
	if err != nil || def == nil {
		t.Fatalf("get role_explore: %v", err)
	}
	edited := *def
	edited.Prompt = "my own prompt"
	if err := srv.store.SaveAgentType(edited); err != nil {
		t.Fatalf("save edit: %v", err)
	}
	if got := status()["role_explore"]; got.Source != string(promptbundle.StatusEdited) {
		t.Fatalf("after edit status = %q, want edited", got.Source)
	}

	// The reset puts the bundled text back and restamps the baseline.
	rec := accountRequest(t, srv, http.MethodPost, "/api/v1/agent-types/role_explore/prompt/reset", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d: %s", rec.Code, rec.Body.String())
	}
	after, err := srv.store.GetAgentType("role_explore")
	if err != nil || after == nil {
		t.Fatalf("get role_explore after reset: %v", err)
	}
	if after.Prompt != explore.Text {
		t.Fatalf("reset left %q, want the bundled text", after.Prompt)
	}
	if got := status()["role_explore"]; got.Source != string(promptbundle.StatusBundled) {
		t.Fatalf("after reset status = %q, want bundled", got.Source)
	}

	// A type with no bundled prompt reports `none` and cannot be reset.
	if err := srv.store.SaveAgentType(agentTypeWithoutBundle()); err != nil {
		t.Fatalf("create custom type: %v", err)
	}
	if got := status()["custom_type"]; got.Source != string(promptbundle.StatusNone) {
		t.Fatalf("custom_type status = %q, want none", got.Source)
	}
	rec = accountRequest(t, srv, http.MethodPost, "/api/v1/agent-types/custom_type/prompt/reset", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("reset of an unbundled type = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
