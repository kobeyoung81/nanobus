package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/agenttypes"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// seededTypes is the platform catalog as the binary ships it: session agents
// (build/plan), subagents (output=caller) and utility helpers (oneshot).
func seededTypes(t *testing.T, s *Server) {
	t.Helper()
	if _, err := s.store.SeedAgentTypes(agenttypes.Definitions()); err != nil {
		t.Fatalf("seed agent types: %v", err)
	}
}

// typeIDs decodes a types response into a set of ids.
func typeIDs(t *testing.T, body []byte) map[string]nanoprotocol.AgentTypeDef {
	t.Helper()
	var resp nanoprotocol.AgentTypesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode types: %v (%s)", err, body)
	}
	out := map[string]nanoprotocol.AgentTypeDef{}
	for _, def := range resp.Types {
		out[def.ID] = def
	}
	return out
}

// hasHelper reports whether the set holds a subagent or a utility definition.
func hasHelper(types map[string]nanoprotocol.AgentTypeDef) bool {
	for _, def := range types {
		if def.Output == nanoprotocol.OutputCaller || def.Strategy == nanoprotocol.StrategyOneshot {
			return true
		}
	}
	return false
}

// TestAdminAgentTypesAreTheFullSet: the admin page builds its type switcher from
// this list, so it must keep seeing subagents and utility helpers. This is the
// regression the user-facing narrowing could have caused.
func TestAdminAgentTypesAreTheFullSet(t *testing.T) {
	s := testServerMongo(t)
	seededTypes(t, s)

	rec := accountRequest(t, s, http.MethodGet, "/api/v1/agent-types", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list = %d: %s", rec.Code, rec.Body.String())
	}
	types := typeIDs(t, rec.Body.Bytes())
	if !hasHelper(types) {
		t.Fatalf("the admin catalog must include helper definitions, got %v", keysOf(types))
	}
	if _, ok := types["role_explore"]; !ok {
		t.Fatalf("role_explore missing from the admin catalog: %v", keysOf(types))
	}
}

// TestUserAgentTypesAreSessionOnly: the user catalog covers an agent and its
// modes only, and the query string is not a way to widen it.
func TestUserAgentTypesAreSessionOnly(t *testing.T) {
	s := testServerMongo(t)
	seededTypes(t, s)

	for _, target := range []string{"/api/v1/user/agent-types", "/api/v1/user/agent-types?all=true"} {
		rec := accountRequest(t, s, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", target, rec.Code, rec.Body.String())
		}
		types := typeIDs(t, rec.Body.Bytes())
		if len(types) == 0 {
			t.Fatalf("%s returned nothing", target)
		}
		if hasHelper(types) {
			t.Fatalf("%s must not offer helper definitions, got %v", target, keysOf(types))
		}
		for _, id := range []string{"build", "plan"} {
			if _, ok := types[id]; !ok {
				t.Fatalf("%s is missing the session type %q: %v", target, id, keysOf(types))
			}
		}
	}
}

// TestWSListAgentTypesHonoursSessionOnly: the RPC path takes session_only from
// the caller and must keep honouring it in both directions.
func TestWSListAgentTypesHonoursSessionOnly(t *testing.T) {
	s := testServerMongo(t)
	seededTypes(t, s)

	list := func(sessionOnly bool) map[string]nanoprotocol.AgentTypeDef {
		t.Helper()
		msg, err := nanoprotocol.NewMessage(nanoprotocol.TypeListAgentTypes,
			nanoprotocol.ListAgentTypesRequest{SessionOnly: sessionOnly})
		if err != nil {
			t.Fatalf("build message: %v", err)
		}
		resp := s.handleListAgentTypes(msg)
		if resp == nil || resp.Type == nanoprotocol.TypeError {
			t.Fatalf("list agent types failed: %+v", resp)
		}
		var body nanoprotocol.AgentTypesResponse
		if err := resp.DecodePayload(&body); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		out := map[string]nanoprotocol.AgentTypeDef{}
		for _, def := range body.Types {
			out[def.ID] = def
		}
		return out
	}

	if all := list(false); !hasHelper(all) {
		t.Fatalf("session_only=false must include helpers, got %v", keysOf(all))
	}
	if session := list(true); hasHelper(session) {
		t.Fatalf("session_only=true must exclude helpers, got %v", keysOf(session))
	}
}

// TestSnapshotIncludesHelperDefinitions: the runtime gets its subagent profiles
// from the snapshot's definition list, not from the user catalog. If this list
// were narrowed, delegation would stop working.
func TestSnapshotIncludesHelperDefinitions(t *testing.T) {
	s := storetest.Connect(t)
	if _, err := s.SeedAgentTypes(agenttypes.Definitions()); err != nil {
		t.Fatalf("seed agent types: %v", err)
	}
	storetest.SeedAgents(t, s, "user-1", "agent-1")

	defs, err := s.ResolveAgentDefinitions("agent-1")
	if err != nil {
		t.Fatalf("resolve definitions: %v", err)
	}
	var caller, oneshot bool
	for _, def := range defs {
		if def.Output == nanoprotocol.OutputCaller {
			caller = true
		}
		if def.Strategy == nanoprotocol.StrategyOneshot {
			oneshot = true
		}
	}
	if !caller {
		t.Fatalf("the snapshot list has no subagent definition: %d definitions", len(defs))
	}
	if !oneshot {
		t.Fatalf("the snapshot list has no utility definition: %d definitions", len(defs))
	}
}

func keysOf(types map[string]nanoprotocol.AgentTypeDef) []string {
	out := make([]string, 0, len(types))
	for id := range types {
		out = append(out, id)
	}
	return out
}
