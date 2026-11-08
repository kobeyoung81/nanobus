package store

import (
	"strings"
	"testing"
)

func TestSettingsToConfigItems(t *testing.T) {
	items := SettingsToConfigItems(map[string]string{
		"max_tool_result_chars": "8000",
		"memory_recent_days":    "3",
		"shell_timeout":         "30s",
		"restrict_to_workspace": "false",
		"not_a_real_key":        "x",
		"empty_value":           "",
	}, 15)

	got := map[string]string{}
	for _, item := range items {
		got[item.Key] = item.Value
	}

	want := map[string]string{
		"agent.maxIterations":       "15",
		"agent.maxToolResultChars":  "8000",
		"agent.memoryRecentDays":    "3",
		"tools.shell.timeout":       "30s",
		"tools.restrictToWorkspace": "false",
	}
	// Prompts belong to agent_types and reach the agent through the definitions
	// snapshot; the config plane must never project one again.
	for _, item := range items {
		if strings.Contains(item.Key, "rompt") {
			t.Errorf("prompt leaked into the projected config items: %s", item.Key)
		}
	}

	for key, value := range want {
		if got[key] != value {
			t.Errorf("config item %s = %q, want %q", key, got[key], value)
		}
	}
	// maxIterations + 4 valid settings (unknown/empty are dropped).
	if len(items) != 5 {
		t.Fatalf("expected 5 projected items, got %d: %+v", len(items), items)
	}
}

func TestSettingsToConfigItems_Empty(t *testing.T) {
	if items := SettingsToConfigItems(nil, 0); len(items) != 0 {
		t.Fatalf("expected no items, got %d", len(items))
	}
}
