package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

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

// SetAgentSettings replaces an agent's runtime override map, as documented on
// UpdateAgentRequest.Settings. An empty (or all-empty) map clears the column.
// Keys outside AgentSettingKeys, or values a key does not accept, are refused
// with ErrUnsupportedAgentSetting so the caller can answer 400.
func (s *Store) SetAgentSettings(agentID string, settings map[string]string) error {
	cleaned := map[string]string{}
	for k, v := range settings {
		if !ValidAgentSettingValue(k, v) {
			return fmt.Errorf("%w: %s=%q", ErrUnsupportedAgentSetting, k, v)
		}
		if v != "" {
			cleaned[k] = v
		}
	}
	var payload string
	if len(cleaned) > 0 {
		b, err := json.Marshal(cleaned)
		if err != nil {
			return fmt.Errorf("marshal agent settings: %w", err)
		}
		payload = string(b)
	}
	ctx, cancel := opCtx()
	defer cancel()

	// The settings belong to the agent document, which lives in MongoDB.
	// Stored as an object rather than the SQL text column: a settings map is
	// what the document holds, and an empty map is a cleared setting.
	set := bson.D{{Key: "settings", Value: bson.D{}}}
	if payload != "" {
		var parsed map[string]string
		if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
			return fmt.Errorf("set agent settings: %w", err)
		}
		set = bson.D{{Key: "settings", Value: parsed}}
	}
	set = append(set, bson.E{Key: "updated_at", Value: time.Now().UTC()})

	res, err := s.db.Collection(agentsCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: agentID}}, bson.D{{Key: "$set", Value: set}})
	if err != nil {
		return fmt.Errorf("set agent settings: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrAgentNotFound
	}
	return nil
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
