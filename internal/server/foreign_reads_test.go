package server

// The owners answer; the handlers ask through one file.
//
// Every domain answers over an internal API now: the agent rows are the portal's,
// the catalogue is the admin module's, the fleet and the queue are the
// provisioner's. Each client lives on the server so that every call has exactly one
// home — runtime_resolution.go — which is what makes the placement auditable.
//
// This test is the line: a new handler that wires its own call fails here. The
// provisioner's write and relay surfaces (runner registration, tunnel forwarding)
// are the bus's own protocol and are not covered: they are not reads of another
// module's rows.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandlersAskTheOwnersThroughOneFile(t *testing.T) {
	ownerCalls := []string{"s.agentSource.", "s.catalogue."}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	allowed := map[string]bool{
		// the one place a foreign read lives
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
		for _, call := range ownerCalls {
			if !strings.Contains(text, call) {
				continue
			}
			t.Errorf("%s calls %s: the call belongs in runtime_resolution.go, next to the other lookups", file, call)
		}
	}
}
