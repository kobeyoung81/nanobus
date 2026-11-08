package agenttypes

import "testing"

// TestDefinitions_RetiredTypesAreGone pins the retirement that migration 063
// carried out. The catalog is seeded on every boot, so a definition that comes
// back here cannot be deleted from the admin UI either (`DeleteAgentType`
// refuses seeded rows) — it simply reappears in the admin catalogue.
func TestDefinitions_RetiredTypesAreGone(t *testing.T) {
	retired := map[string]bool{"role_background": true, "role_subagent": true}
	for _, def := range Definitions() {
		if retired[def.ID] {
			t.Errorf("%s is retired but the catalog still ships it", def.ID)
		}
	}
}

// TestDefinitions_ShippedSurface checks the shapes the runtime resolves by
// execution attributes: two session modes, three subagent profiles, and the
// utility workers.
func TestDefinitions_ShippedSurface(t *testing.T) {
	want := []string{
		"build", "plan",
		"role_general", "role_explore", "role_scout",
		"role_title", "role_summarize", "utility_memory_flush",
	}
	got := map[string]bool{}
	for _, def := range Definitions() {
		if got[def.ID] {
			t.Errorf("duplicate definition id %q", def.ID)
		}
		got[def.ID] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("%s is missing from the catalog", id)
		}
	}
	if len(got) != len(want) {
		t.Errorf("catalog has %d definitions, want %d: %v", len(got), len(want), got)
	}
}
