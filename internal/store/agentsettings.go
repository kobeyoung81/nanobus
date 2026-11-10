package store

import (
	"errors"
	"fmt"
	"strconv"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// EffectiveAgentSettings resolves the runtime settings for an agent's primary
// type: the AgentType baseline overlaid by the user's per-(agent, type)
// override. Empty values are dropped so the runtime falls back to its code
// defaults. Non-default modes are applied per turn from each definition.
func (s *Store) EffectiveAgentSettings(agentID string) (map[string]string, error) {
	out := map[string]string{}

	agent, err := s.GetAgent(agentID)
	if err != nil {
		return nil, fmt.Errorf("effective agent settings: %w", err)
	}
	if agent == nil {
		return out, nil
	}
	typeID := agent.TypeID
	if typeID == "" {
		typeID = "build"
	}

	// Two steps rather than one join: the type baseline and the user's override
	// are separate collections, as they were separate tables.
	//
	// A missing type yields no settings *and* drops the override, which is what
	// the LEFT JOIN did when its driving row was absent.
	t, err := s.GetAgentType(typeID)
	if err != nil {
		return nil, fmt.Errorf("effective agent settings: %w", err)
	}
	if t == nil {
		return out, nil
	}
	ov, err := s.GetAgentTypeOverride(agentID, typeID)
	if err != nil {
		return nil, fmt.Errorf("effective agent settings: %w", err)
	}

	// Empty values are dropped at each layer so the runtime falls back to its
	// code defaults, and the override is applied second so it wins.
	merge := func(m map[string]string) {
		for k, v := range m {
			if v != "" {
				out[k] = v
			}
		}
	}
	merge(t.Settings)
	if ov != nil {
		merge(ov.Settings)
	}
	return out, nil
}

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
	return items
}
