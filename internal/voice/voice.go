// Package voice talks to OpenAI-compatible speech providers for the browser
// composer's voice input, in three tiers:
//
//	realtime  the layer the composer streams into while the user talks — the
//	          platform's own ASR service by default, an external realtime
//	          provider when configured that way;
//	batch     a second, higher-accuracy pass over the whole recording, run by an
//	          external file-transcription model when the take ends;
//	polish    a chat model that rewrites the winning transcript into a sendable
//	          message.
//
// Platform configuration is a single set of service_config_items under the
// "voice" component, so the REST, WebSocket and admin surfaces all read the same
// settings through LoadConfig.
package voice

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/llmprobe"
	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// Component is the service_config_items component type holding the settings.
const Component = "voice"

// Engine values for the realtime tier. The engine only decides which layer the
// platform expects; the provider it points at is what actually serves it.
const (
	EngineLocal  = "local"
	EngineRemote = "remote"
)

// Triggers for the batch tier.
const (
	// BatchAlways re-transcribes every take.
	BatchAlways = "always"
	// BatchManual leaves the pass to the user (the composer offers it).
	BatchManual = "manual"
	// BatchEmpty runs it only when the live transcript came back empty or
	// suspiciously short for how long the user spoke.
	BatchEmpty = "empty"
)

// Config is the platform-wide voice input configuration.
type Config struct {
	Enabled bool
	// Shortcut is the key combination that starts and stops a dictation, in the
	// browser's own notation (e.g. "Alt+M"). It is a platform default: the
	// composer lets each user override it for their own keyboard, and an empty
	// value means the platform offers no shortcut at all. The bus stores the
	// string as written — validating it is the browser's job, since the browser
	// is what has to match it against a key event.
	Shortcut string
	Realtime RealtimeConfig
	Batch    BatchConfig
	Polish   PolishConfig
}

// RealtimeConfig is tier 1: the session the composer streams into.
type RealtimeConfig struct {
	// Engine is "local" (the platform's own ASR service) or "remote" (any
	// OpenAI-compatible realtime provider).
	Engine           string
	ProviderID       int64
	Model            string
	Language         string
	Prompt           string
	MaxSeconds       int
	CommitIntervalMS int
	// AudioRate is the rate the composer captures at and declares to the
	// provider. 24 kHz is what the OpenAI realtime contract takes; a local model
	// that wants 16 kHz says so here instead of being resampled on the way in.
	AudioRate int
}

// BatchConfig is tier 2: the whole-recording pass.
type BatchConfig struct {
	Enabled    bool
	Trigger    string
	ProviderID int64
	Model      string
	Language   string
	Prompt     string
}

// PolishConfig is tier 3: the rewrite pass.
type PolishConfig struct {
	Enabled bool
	ModelID int64
	Prompt  string
	// Thinking caps the polish model's reasoning. "none" is the explicit "do
	// not think", which is the only shape that works: leaving the parameter out
	// still lets a reasoning model think, so off has to be said out loud.
	Thinking string
	// SkipAfterBatch drops the polish pass when the batch model already
	// rewrote the text.
	SkipAfterBatch bool
}

// Defaults and bounds. The bounds keep a mistyped config item from producing an
// unbounded recording or a commit storm.
const (
	DefaultMaxSeconds   = 120
	DefaultLanguage     = "zh"
	MinMaxSeconds       = 10
	MaxMaxSeconds       = 600
	MaxCommitIntervalMS = 2000

	// DefaultShortcut is the platform default seeded into service_config_items:
	// Alt+M. Chrome leaves it alone (Ctrl+Shift+D, the obvious "dictate"
	// shortcut, is "bookmark all tabs" there).
	DefaultShortcut = "Alt+M"

	// DefaultPolishPrompt is seeded into service_config_items and editable from
	// the portal's voice card.
	DefaultPolishPrompt = "你是语音输入的后处理助手。把用户口述的文本整理成可以直接发送的消息：" +
		"补全标点、纠正明显的同音与识别错字、去掉口头语与重复，保留原意、语气和语言（中文就用中文）。" +
		"不要添加原文没有的信息，不要解释，不要加引号或代码块，只输出整理后的文本。"
)

// LoadConfig reads the voice settings from the component's config items,
// applying defaults for absent keys and clamping the numeric bounds.
func LoadConfig(items []store.ConfigItem) Config {
	m := store.ConfigItemsToMap(items)

	cfg := Config{
		Enabled:  boolValue(m, "enabled", false),
		Shortcut: DefaultShortcut,
		Realtime: RealtimeConfig{
			Engine:           normalizeEngine(m["realtime.engine"]),
			ProviderID:       int64Value(m, "realtime.providerId", 0),
			Model:            strings.TrimSpace(m["realtime.model"]),
			Language:         DefaultLanguage,
			Prompt:           strings.TrimSpace(m["realtime.prompt"]),
			MaxSeconds:       intValue(m, "realtime.maxSeconds", DefaultMaxSeconds),
			CommitIntervalMS: intValue(m, "realtime.commitIntervalMs", 0),
			AudioRate:        intValue(m, "realtime.audioRate", AudioRate),
		},
		Batch: BatchConfig{
			Enabled:    boolValue(m, "batch.enabled", false),
			Trigger:    normalizeTrigger(m["batch.trigger"]),
			ProviderID: int64Value(m, "batch.providerId", 0),
			Model:      strings.TrimSpace(m["batch.model"]),
			Language:   DefaultLanguage,
			Prompt:     strings.TrimSpace(m["batch.prompt"]),
		},
		Polish: PolishConfig{
			Enabled:        boolValue(m, "polish.enabled", true),
			ModelID:        int64Value(m, "polish.modelId", 0),
			Prompt:         DefaultPolishPrompt,
			Thinking:       normalizeThinking(m["polish.thinking"]),
			SkipAfterBatch: boolValue(m, "polish.skipAfterBatch", false),
		},
	}
	// An explicitly empty language means "let the provider detect it", which is
	// different from the key being absent.
	if v, ok := m["realtime.language"]; ok {
		cfg.Realtime.Language = strings.TrimSpace(v)
	}
	// An explicitly empty shortcut means "the platform offers none", which is
	// different from the key being absent.
	if v, ok := m["shortcut"]; ok {
		cfg.Shortcut = strings.TrimSpace(v)
	}
	if v, ok := m["batch.language"]; ok {
		cfg.Batch.Language = strings.TrimSpace(v)
	}
	if v := strings.TrimSpace(m["polish.prompt"]); v != "" {
		cfg.Polish.Prompt = v
	}

	cfg.Realtime.MaxSeconds = clamp(cfg.Realtime.MaxSeconds, MinMaxSeconds, MaxMaxSeconds)
	cfg.Realtime.CommitIntervalMS = clamp(cfg.Realtime.CommitIntervalMS, 0, MaxCommitIntervalMS)
	cfg.Realtime.AudioRate = clamp(cfg.Realtime.AudioRate, MinAudioRate, MaxAudioRate)
	return cfg
}

// Available reports whether dictation can run at all, and a stable reason code
// when it cannot ("disabled" | "not_configured").
//
// The realtime tier is required even when the batch tier is on: capture is
// driven by the realtime session, so without it the composer has nothing to
// record into. A realtime layer that is configured but unreachable is a runtime
// failure, and that is the case the batch pass exists to cover.
func (c Config) Available() (bool, string) {
	if !c.Enabled {
		return false, "disabled"
	}
	if !c.Realtime.Available() {
		return false, "not_configured"
	}
	return true, ""
}

// Available reports whether the realtime tier is configured.
func (r RealtimeConfig) Available() bool {
	return r.ProviderID != 0 && strings.TrimSpace(r.Model) != ""
}

// Available reports whether the batch tier may be used.
func (b BatchConfig) Available() bool {
	return b.Enabled && b.ProviderID != 0 && strings.TrimSpace(b.Model) != ""
}

// Provider is the connection detail resolved from the catalog for one call.
type Provider struct {
	ID      int64
	Name    string
	Kind    string
	BaseURL string
	APIKey  string
}

// AudioRate is the sample rate the realtime API accepts. Anything else is
// rejected upstream, so the browser captures (or resamples to) exactly this. It
// is also the default for the realtime.audioRate config item: a local engine that
// wants 16 kHz sets that key rather than being resampled on the way in.
const AudioRate = 24000

// Bounds for the configured rate: below the first no speech model works, and
// above the second no browser captures.
const (
	MinAudioRate = 8000
	MaxAudioRate = 48000
)

// RealtimeURL builds the provider's realtime WebSocket URL for a transcription
// session: the base URL's scheme is switched to ws/wss and
// "realtime?model=…&intent=transcription" is appended.
func RealtimeURL(baseURL, model string) (string, error) {
	base, err := wsBase(baseURL)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	q := u.Query()
	q.Set("model", model)
	// LiteLLM routes transcription sessions to the GA endpoint only.
	q.Set("intent", "transcription")
	u.Path = strings.TrimRight(u.Path, "/") + "/realtime"
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// TranscriptionURL and ChatURL are the one-shot REST endpoints of the same
// provider (the batch pass and the admin test button, and the polish pass).
func TranscriptionURL(baseURL string) string {
	return llmprobe.Endpoint(baseURL, "audio/transcriptions")
}
func ChatURL(baseURL string) string { return llmprobe.Endpoint(baseURL, "chat/completions") }

// wsBase validates a provider base URL and switches its scheme to ws/wss.
func wsBase(baseURL string) (string, error) {
	base := strings.TrimSpace(baseURL)
	if base == "" {
		return "", fmt.Errorf("base URL is required")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("base URL must be an http(s) URL")
	}
	return u.String(), nil
}

// CallTimeout bounds a single provider call; the HTTP client and the WebSocket
// handler share it so a hung provider cannot pin a connection forever.
const CallTimeout = 60 * time.Second

// EffortNone is the explicit "do not reason" effort. A reasoning model keeps
// thinking when the parameter is missing and when it carries a low level, so
// this is the only value that turns it off.
const EffortNone = "none"

// ThinkingEfforts are the reasoning caps a chat model may be asked for. An
// unrecognized value falls back to EffortNone rather than being passed
// upstream.
var ThinkingEfforts = []string{EffortNone, "low", "medium", "high"}

// normalizeThinking validates the configured reasoning effort. Anything that is
// not a recognized level — including the empty value the key held before off
// was expressed explicitly — reads as EffortNone: an operator who leaves this
// on "off" must get a model that really stops thinking.
func normalizeThinking(v string) string {
	effort := strings.ToLower(strings.TrimSpace(v))
	for _, allowed := range ThinkingEfforts {
		if effort == allowed {
			return effort
		}
	}
	return EffortNone
}

// normalizeEngine validates the realtime engine. An unrecognized value falls
// back to the platform default rather than leaving the tier undefined.
func normalizeEngine(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case EngineRemote:
		return EngineRemote
	default:
		return EngineLocal
	}
}

// normalizeTrigger validates the batch trigger. "always" is the platform
// default, so a mistyped value re-transcribes rather than silently skipping.
func normalizeTrigger(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case BatchManual:
		return BatchManual
	case BatchEmpty:
		return BatchEmpty
	default:
		return BatchAlways
	}
}

func boolValue(m map[string]string, key string, def bool) bool {
	v, ok := m[key]
	if !ok {
		return def
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return parsed
}

func int64Value(m map[string]string, key string, def int64) int64 {
	v, ok := m[key]
	if !ok {
		return def
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return def
	}
	return parsed
}

func intValue(m map[string]string, key string, def int) int {
	v, ok := m[key]
	if !ok {
		return def
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return parsed
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
