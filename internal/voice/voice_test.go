package voice

import (
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

func items(pairs map[string]string) []store.ConfigItem {
	out := make([]store.ConfigItem, 0, len(pairs))
	for key, value := range pairs {
		out = append(out, store.ConfigItem{Key: key, Value: value})
	}
	return out
}

func TestLoadConfigDefaultsToThreeIdleTiers(t *testing.T) {
	cfg := LoadConfig(nil)

	if cfg.Enabled {
		t.Error("voice input must stay off until an operator enables it")
	}
	if cfg.Shortcut != DefaultShortcut {
		t.Errorf("Shortcut = %q, want the platform default %q", cfg.Shortcut, DefaultShortcut)
	}
	if cfg.Realtime.MaxSeconds != DefaultMaxSeconds || cfg.Realtime.Language != DefaultLanguage {
		t.Errorf("realtime defaults: %+v", cfg.Realtime)
	}
	if cfg.Realtime.CommitIntervalMS != 0 {
		t.Errorf("CommitIntervalMS = %d, want 0 (commit when the user stops)", cfg.Realtime.CommitIntervalMS)
	}
	if cfg.Batch.Enabled || cfg.Batch.Trigger != BatchAlways {
		t.Errorf("batch defaults: %+v", cfg.Batch)
	}
	if !cfg.Polish.Enabled || cfg.Polish.Prompt != DefaultPolishPrompt || cfg.Polish.Thinking != EffortNone {
		t.Errorf("polish defaults: %+v", cfg.Polish)
	}
	if cfg.Polish.SkipAfterBatch {
		t.Error("polish must still run after the batch pass by default")
	}
	if ok, reason := cfg.Available(); ok || reason != "disabled" {
		t.Errorf("Available = %v/%q, want false/disabled", ok, reason)
	}
}

// TestLoadConfigShortcut pins the three states an operator can express: a
// value, an explicit empty one (the platform offers no shortcut, and no built-in
// default may be substituted for it), and the key being absent (the default).
func TestLoadConfigShortcut(t *testing.T) {
	if got := LoadConfig(items(map[string]string{"shortcut": "Ctrl+Alt+K"})).Shortcut; got != "Ctrl+Alt+K" {
		t.Errorf("Shortcut = %q, want %q", got, "Ctrl+Alt+K")
	}
	if got := LoadConfig(items(map[string]string{"shortcut": "   "})).Shortcut; got != "" {
		t.Errorf("an explicitly empty shortcut must stay empty, got %q", got)
	}
	if got := LoadConfig(nil).Shortcut; got != DefaultShortcut {
		t.Errorf("Shortcut = %q, want the default %q", got, DefaultShortcut)
	}
}

func TestLoadConfigReadsEveryTier(t *testing.T) {
	cfg := LoadConfig(items(map[string]string{
		"enabled":                   "true",
		"shortcut":                  " ctrl+shift+m ",
		"realtime.providerId":       "7",
		"realtime.model":            " gpt-4o-transcribe ",
		"realtime.language":         "",
		"realtime.prompt":           "术语：myriadmind",
		"realtime.maxSeconds":       "300",
		"realtime.commitIntervalMs": "500",
		"batch.enabled":             "true",
		"batch.trigger":             "manual",
		"batch.providerId":          "9",
		"batch.model":               "whisper-1",
		"batch.language":            "en",
		"batch.prompt":              "技术名词",
		"polish.enabled":            "false",
		"polish.modelId":            "3",
		"polish.prompt":             "只改标点",
		"polish.thinking":           "HIGH",
		"polish.skipAfterBatch":     "true",
	}))

	if !cfg.Enabled {
		t.Fatal("enabled was not read")
	}
	if cfg.Shortcut != "ctrl+shift+m" {
		t.Errorf("Shortcut = %q, want the stored value with the padding trimmed", cfg.Shortcut)
	}
	if cfg.Realtime.ProviderID != 7 || cfg.Realtime.Model != "gpt-4o-transcribe" {
		t.Errorf("realtime: %+v", cfg.Realtime)
	}
	if cfg.Realtime.Language != "" {
		t.Errorf("Language = %q, want empty (auto-detect)", cfg.Realtime.Language)
	}
	if cfg.Realtime.MaxSeconds != 300 || cfg.Realtime.CommitIntervalMS != 500 {
		t.Errorf("realtime bounds: %+v", cfg.Realtime)
	}
	if !cfg.Batch.Enabled || cfg.Batch.Trigger != BatchManual || cfg.Batch.ProviderID != 9 || cfg.Batch.Model != "whisper-1" {
		t.Errorf("batch: %+v", cfg.Batch)
	}
	if cfg.Batch.Language != "en" || cfg.Batch.Prompt != "技术名词" {
		t.Errorf("batch hints: %+v", cfg.Batch)
	}
	if cfg.Polish.Enabled || cfg.Polish.ModelID != 3 || cfg.Polish.Prompt != "只改标点" {
		t.Errorf("polish: %+v", cfg.Polish)
	}
	if cfg.Polish.Thinking != "high" {
		t.Errorf("Thinking = %q, want the normalized %q", cfg.Polish.Thinking, "high")
	}
	if !cfg.Polish.SkipAfterBatch {
		t.Error("skipAfterBatch was not read")
	}
}

func TestLoadConfigClampsBounds(t *testing.T) {
	cfg := LoadConfig(items(map[string]string{
		"realtime.providerId":       "1",
		"realtime.model":            "m",
		"realtime.maxSeconds":       "99999",
		"realtime.commitIntervalMs": "99999",
	}))
	if cfg.Realtime.MaxSeconds != MaxMaxSeconds {
		t.Errorf("MaxSeconds = %d, want clamp to %d", cfg.Realtime.MaxSeconds, MaxMaxSeconds)
	}
	if cfg.Realtime.CommitIntervalMS != MaxCommitIntervalMS {
		t.Errorf("CommitIntervalMS = %d, want clamp to %d", cfg.Realtime.CommitIntervalMS, MaxCommitIntervalMS)
	}

	cfg = LoadConfig(items(map[string]string{
		"realtime.maxSeconds":       "1",
		"realtime.commitIntervalMs": "-5",
	}))
	if cfg.Realtime.MaxSeconds != MinMaxSeconds {
		t.Errorf("MaxSeconds = %d, want clamp to %d", cfg.Realtime.MaxSeconds, MinMaxSeconds)
	}
	if cfg.Realtime.CommitIntervalMS != 0 {
		t.Errorf("CommitIntervalMS = %d, want clamp to 0", cfg.Realtime.CommitIntervalMS)
	}
}

// TestUnrecognizedEnumsFallBackToThePlatformDefault keeps a mistyped value from
// silently skipping a tier: an unknown batch trigger re-transcribes.
func TestUnrecognizedEnumsFallBackToThePlatformDefault(t *testing.T) {
	cfg := LoadConfig(items(map[string]string{
		"batch.trigger": "sometimes",
	}))
	if cfg.Batch.Trigger != BatchAlways {
		t.Errorf("Trigger = %q, want %q", cfg.Batch.Trigger, BatchAlways)
	}
}

// TestThinkingEffortIsValidated pins the closed set of levels and, more
// importantly, that "off" is an explicit value rather than an absent one: the
// empty key an unedited platform holds, and any typo, both have to read as
// "none" or the polish pass keeps thinking.
func TestThinkingEffortIsValidated(t *testing.T) {
	for value, want := range map[string]string{
		"":        EffortNone,
		"none":    EffortNone,
		"NONE":    EffortNone,
		"low":     "low",
		"HIGH":    "high",
		" Medium": "medium",
		"on":      EffortNone,
		"medium2": EffortNone,
	} {
		if got := normalizeThinking(value); got != want {
			t.Errorf("polish.thinking %q = %q, want %q", value, got, want)
		}
	}
}

// TestAvailabilityFollowsTheRealtimeTier pins the rule that the batch pass alone
// never shows a microphone: capture is driven by the realtime session, so a
// platform with only a batch model has nothing to record into.
func TestAvailabilityFollowsTheRealtimeTier(t *testing.T) {
	cases := []struct {
		name string
		keys map[string]string
		ok   bool
	}{
		{"disabled", map[string]string{"enabled": "false", "realtime.providerId": "1", "realtime.model": "m"}, false},
		{"no realtime provider", map[string]string{"enabled": "true", "realtime.model": "m"}, false},
		{"no realtime model", map[string]string{"enabled": "true", "realtime.providerId": "1"}, false},
		{"realtime only", map[string]string{"enabled": "true", "realtime.providerId": "1", "realtime.model": "m"}, true},
		{
			"batch alone is not enough",
			map[string]string{"enabled": "true", "batch.enabled": "true", "batch.providerId": "2", "batch.model": "w"},
			false,
		},
		{
			"batch is an extra on top of realtime",
			map[string]string{
				"enabled": "true", "realtime.providerId": "1", "realtime.model": "m",
				"batch.enabled": "true", "batch.providerId": "2", "batch.model": "w",
			},
			true,
		},
	}
	for _, tc := range cases {
		cfg := LoadConfig(items(tc.keys))
		if ok, _ := cfg.Available(); ok != tc.ok {
			t.Errorf("%s: Available = %v, want %v", tc.name, ok, tc.ok)
		}
	}
}

// TestBatchAvailabilityNeedsAProviderAndModel: the switch alone is not enough,
// and an empty model must not reach a provider.
func TestBatchAvailabilityNeedsAProviderAndModel(t *testing.T) {
	for name, keys := range map[string]map[string]string{
		"off":         {"batch.enabled": "false", "batch.providerId": "2", "batch.model": "w"},
		"no provider": {"batch.enabled": "true", "batch.model": "w"},
		"no model":    {"batch.enabled": "true", "batch.providerId": "2"},
	} {
		if cfg := LoadConfig(items(keys)); cfg.Batch.Available() {
			t.Errorf("%s: batch must not be available", name)
		}
	}
	cfg := LoadConfig(items(map[string]string{"batch.enabled": "true", "batch.providerId": "2", "batch.model": " w "}))
	if !cfg.Batch.Available() {
		t.Error("a configured batch tier must be available")
	}
}
