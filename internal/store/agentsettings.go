package store

import (
	"errors"
	"sort"
	"strconv"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// ErrUnsupportedAgentSetting reports a settings key or value the platform does
// not accept from a client.
var ErrUnsupportedAgentSetting = errors.New("unsupported agent setting")

// AgentSettingKeys are the per-agent settings a user-facing client may write
// through the agent update endpoint. The list is deliberately short: the column
// is a general store, and a UI-facing write path must not become a way to set
// arbitrary runtime policy.
var AgentSettingKeys = map[string]bool{
	"scheduled_task_mode": true,
}

// ValidAgentSettingValue reports whether value is usable for key. An empty
// value is always allowed: it means "inherit" and is dropped on read.
func ValidAgentSettingValue(key, value string) bool {
	if !AgentSettingKeys[key] {
		return false
	}
	if value == "" {
		return true
	}
	switch key {
	case "scheduled_task_mode":
		return value == "build" || value == "plan"
	}
	return false
}

// agentSettingToConfigKey maps a settings key to the flat runtime config key
// the agent consumes (see nanosec/internal/config/loader.go).
var agentSettingToConfigKey = map[string]string{
	"max_tool_result_chars":       "agent.maxToolResultChars",
	"summarize_token_percent":     "agent.summarizeTokenPercent",
	"summarize_message_threshold": "agent.summarizeMessageThreshold",
	"title_refresh_messages":      "agent.titleRefreshMessages",
	"memory_recent_days":          "agent.memoryRecentDays",
	"restrict_to_workspace":       "tools.restrictToWorkspace",
	"shell_timeout":               "tools.shell.timeout",
}

// SettingsToConfigItems projects effective settings (plus max_rounds) onto the
// flat runtime config keys, so the agent's existing config loader keeps working
// unchanged. Prompts are deliberately absent: they live in agent_types.prompt
// and reach the agent through the definitions snapshot, never the config plane.
func SettingsToConfigItems(settings map[string]string, maxRounds int) []nanoprotocol.ConfigItem {
	items := []nanoprotocol.ConfigItem{}
	if maxRounds > 0 {
		items = append(items, nanoprotocol.ConfigItem{Key: "agent.maxIterations", Value: strconv.Itoa(maxRounds), ValueType: "int"})
	}
	for key, value := range settings {
		if value == "" {
			continue
		}
		cfgKey, ok := agentSettingToConfigKey[key]
		if !ok {
			continue
		}
		items = append(items, nanoprotocol.ConfigItem{Key: cfgKey, Value: value, ValueType: "string"})
	}
	// A map has no order, and this slice ends up in the agent's snapshot: sorting
	// keeps two identical reads identical, which is what makes a snapshot
	// comparable at all.
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	return items
}
