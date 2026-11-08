package main

import "testing"

func TestDefaultConfigItems_VoiceComponent(t *testing.T) {
	items := defaultConfigItems()["voice"]
	if len(items) == 0 {
		t.Fatal("the voice component must be seeded so the portal can edit it")
	}
	keys := make(map[string]string, len(items))
	for _, item := range items {
		keys[item.Key] = item.Value
	}

	for key, want := range map[string]string{
		// Off until an operator attaches a provider: an enabled-but-unconfigured
		// voice would show a microphone that cannot work.
		"enabled":                   "false",
		"realtime.engine":           "local",
		"realtime.providerId":       "",
		"realtime.model":            "",
		"realtime.language":         "zh",
		"realtime.maxSeconds":       "120",
		"realtime.commitIntervalMs": "0",
		"realtime.audioRate":        "24000",
		"realtime.hotwords":         "",
		// The batch tier is off until a provider is attached; when it is turned
		// on it re-transcribes every take, which is what makes the accuracy
		// claim real by default.
		"batch.enabled":         "false",
		"batch.trigger":         "always",
		"batch.providerId":      "",
		"batch.model":           "",
		"batch.language":        "zh",
		"polish.enabled":        "true",
		"polish.modelId":        "",
		"polish.skipAfterBatch": "false",
		// Off by default, and stated explicitly: a reasoning model keeps
		// thinking when the parameter is missing, so "off" has to be sent.
		"polish.thinking": "none",
	} {
		got, ok := keys[key]
		if !ok {
			t.Fatalf("missing voice default %q", key)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if keys["polish.prompt"] == "" {
		t.Fatal("the polish prompt must be seeded with a usable default")
	}
}

func TestDefaultConfigItems_AgentUsesPathKeys(t *testing.T) {
	items := defaultConfigItems()["agent"]
	keys := make(map[string]string, len(items))
	for _, item := range items {
		keys[item.Key] = item.Value
	}

	for key, want := range map[string]string{
		"paths.dataRoot": "~/.nanosec",
		// paths.* are per-agent derived when empty (see the seed comments).
		"paths.sessionDir":   "",
		"paths.cronFile":     "",
		"paths.mediaBaseDir": "",
		"paths.workspaceDir": "",
		"paths.identityFile": "",
		"paths.bootstrapDir": "",
		"paths.memoryDir":    "",
		"paths.skillsDir":    "",
	} {
		got, ok := keys[key]
		if !ok {
			t.Fatalf("missing agent default %q", key)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}

	for _, removed := range []string{
		"agent.sessionDir",
		"tools.workspace",
		"media.basePath",
		// Platform LLM config now lives in the model catalog (migration 041).
		"provider.apiKey",
		"provider.baseUrl",
		"provider.defaultModel",
		"agent.defaultModelId",
		"agent.contextWindow",
		"agent.maxOutputTokens",
		"agent.default.provider.titleModel",
		"agent.default.provider.summarizeModel",
		"agent.default.provider.subagentModel",
		// Runtime policy/tool settings moved onto AgentTypes (migration 057).
		"agent.maxIterations",
		"agent.maxToolResultChars",
		"agent.summarizeTokenPercent",
		"agent.summarizeMessageThreshold",
		"agent.memoryRecentDays",
		"agent.sessionMode",
		"agent.sessionIdleTimeout",
		"agent.sessionMaxMessages",
		"agent.default.bootstrapPrompt",
		"tools.restrictToWorkspace",
		"tools.shell.timeout",
		// Web search tool retired in favour of MCP (migration 058).
		"tools.web.searchApiKey",
		"tools.web.maxResults",
	} {
		if _, ok := keys[removed]; ok {
			t.Fatalf("unexpected legacy agent default %q", removed)
		}
	}
}

// The agent container timezone is seeded so it is visible and editable in the
// config center: without it every agent container runs in UTC, which shifts
// prompt dates, cron schedules and git timestamps for a CST deployment.
func TestDefaultConfigItems_AgentTimezoneDefaultsToShanghai(t *testing.T) {
	for _, item := range defaultConfigItems()["runner"] {
		if item.Key != "runner.agentTimezone" {
			continue
		}
		if item.Value != "Asia/Shanghai" {
			t.Fatalf("runner.agentTimezone = %q, want Asia/Shanghai", item.Value)
		}
		if item.Description == "" {
			t.Error("the seed needs a description: the config UI shows it")
		}
		return
	}
	t.Fatal("runner.agentTimezone is not seeded")
}

// The platform default mode for scheduled tasks is seeded so an operator can
// see and change it in the config center without hand-writing a key.
func TestDefaultConfigItems_ScheduledTaskModeDefaultsToBuild(t *testing.T) {
	for _, item := range defaultConfigItems()["agent"] {
		if item.Key != "agent.scheduledTaskMode" {
			continue
		}
		if item.Value != "build" {
			t.Fatalf("agent.scheduledTaskMode = %q, want build", item.Value)
		}
		if item.Description == "" {
			t.Error("the seed needs a description: the config UI shows it")
		}
		return
	}
	t.Fatal("agent.scheduledTaskMode is not seeded")
}
