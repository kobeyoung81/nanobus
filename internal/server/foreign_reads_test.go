package server

// The handlers must not read another module's collections.
//
// Every placement of a domain moved twice: first the rows, then the reads. The
// reads now go through the owner's API (`s.resolveRuntime`, `s.lookupModel`,
// `s.lookupAgentFleet`, …), and the bus's own copy is kept in exactly one file —
// runtime_resolution.go — as the switch a rolling upgrade needs.
//
// This test is the line that keeps a new handler from quietly reading the database
// again: it fails when `s.store.<a foreign read>` appears anywhere else. The rule is
// one sentence — a handler asks the owner, it does not reach past it.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// foreignReads are the store methods that resolve another module's data. Their
// calls belong in runtime_resolution.go (the fallback), nowhere else.
var foreignReads = []string{
	// admin: the catalogue (the agent-row reads are delegated inside the store)
	"s.store.GetAgentType(",
	"s.store.GetAgentTypeOverride(",
	"s.store.ListAgentTypes(",
	"s.store.PlatformSessionHead(",
	"s.store.AgentSessionHead(",
	"s.store.EffectiveSessionHead(",
	"s.store.GetModel(",
	"s.store.GetModelProvider(",
	"s.store.ResolveCapabilities(",
	"s.store.ResolveModeCapabilities(",
	"s.store.EffectiveAgentSettings(",
	"s.store.ResolveAgentDefinitions(",
	"s.store.AgentTypeForAgent(",
	"s.store.DefaultModeModel(",
	"s.store.CapabilityVisible(",
	// portal: the fleet list
	"s.store.ListAgentFleet(",
	// provisioner: the queue and the runners
	"s.store.ListRunners(",
	"s.store.CountOnlineRunners(",
	"s.store.ListContainerSummaries(",
	"s.store.CountContainersByStatus(",
}

func TestHandlersDoNotReadAnotherModulesCollections(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	allowed := map[string]bool{
		// the fallback: every foreign read lives here, behind the switch
		"runtime_resolution.go": true,
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || allowed[file] {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(raw)
		for _, call := range foreignReads {
			if !strings.Contains(text, call) {
				continue
			}
			// Name the file and the call, so the fix is obvious: use the lookup
			// helper, or move the read into runtime_resolution.go.
			t.Errorf("%s calls %s: handlers ask the owner (s.lookup*, s.resolve*), they do not read its collection", file, call)
		}
	}
}
