package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/voice"
	"github.com/gorilla/websocket"
)

func voiceConfigItems(enabled bool, providerID, model, polishModelID string) []store.ConfigItem {
	return []store.ConfigItem{
		{Key: "enabled", Value: fmt.Sprint(enabled), ValueType: "bool"},
		{Key: "shortcut", Value: "Ctrl+Shift+M", ValueType: "string"},
		{Key: "realtime.providerId", Value: providerID, ValueType: "int"},
		{Key: "realtime.model", Value: model, ValueType: "string"},
		{Key: "realtime.language", Value: "zh", ValueType: "string"},
		{Key: "realtime.maxSeconds", Value: "90", ValueType: "int"},
		{Key: "realtime.commitIntervalMs", Value: "0", ValueType: "int"},
		{Key: "polish.enabled", Value: "true", ValueType: "bool"},
		{Key: "polish.modelId", Value: polishModelID, ValueType: "int"},
	}
}

// voiceBatchItems turns the batch tier on: /refine is the endpoint that reads it.
func voiceBatchItems(providerID, model, trigger string) []store.ConfigItem {
	return []store.ConfigItem{
		{Key: "batch.enabled", Value: "true", ValueType: "bool"},
		{Key: "batch.trigger", Value: trigger, ValueType: "string"},
		{Key: "batch.providerId", Value: providerID, ValueType: "int"},
		{Key: "batch.model", Value: model, ValueType: "string"},
		{Key: "batch.language", Value: "zh", ValueType: "string"},
		{Key: "batch.prompt", Value: "术语", ValueType: "string"},
	}
}

// seedVoiceConfig writes the voice component's configuration into the store the
// server reads: seeding real documents is what makes the endpoint's answer depend
// on data rather than on a mock agreeing with the code.
func seedVoiceConfig(t *testing.T, s *Server, enabled bool, providerID, model, polishModelID string) {
	seedVoiceConfigItems(t, s, voiceConfigItems(enabled, providerID, model, polishModelID))
}

func seedVoiceConfigWithBatch(t *testing.T, s *Server, enabled bool, providerID, model, polishModelID, batchProviderID, batchModel, trigger string) {
	items := append(voiceConfigItems(enabled, providerID, model, polishModelID),
		voiceBatchItems(batchProviderID, batchModel, trigger)...)
	seedVoiceConfigItems(t, s, items)
}

func seedVoiceConfigItems(t *testing.T, s *Server, items []store.ConfigItem) {
	t.Helper()
	if err := seedConfig(t, s, voice.Component, items); err != nil {
		t.Fatalf("seed voice config: %v", err)
	}
}

// seedProvider and seedChatModel write the model catalog into the store the
// server reads: seeding real documents is what makes an endpoint's answer depend
// on data rather than on a mock agreeing with the code.
func seedProvider(t *testing.T, s *Server, id int64, scope, name, baseURL, apiKey string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := s.store.DB().Collection("admin_model_providers").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id},
		{Key: "scope", Value: scope},
		{Key: "owner_user_id", Value: ""},
		{Key: "name", Value: name},
		{Key: "kind", Value: "openai"},
		{Key: "base_url", Value: baseURL},
		{Key: "api_key", Value: apiKey},
		{Key: "created_at", Value: now},
		{Key: "updated_at", Value: now},
	}); err != nil {
		t.Fatalf("seed provider %d: %v", id, err)
	}
}

func seedChatModel(t *testing.T, s *Server, id, providerID int64, name, model string) {
	t.Helper()
	now := time.Now().UTC()
	if _, err := s.store.DB().Collection("admin_models").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: id},
		{Key: "provider_id", Value: providerID},
		{Key: "scope", Value: "platform"},
		{Key: "owner_user_id", Value: ""},
		{Key: "name", Value: name},
		{Key: "model", Value: model},
		{Key: "context_window", Value: 128000},
		{Key: "max_output_tokens", Value: 4096},
		{Key: "thinking_enabled", Value: false},
		{Key: "created_at", Value: now},
		{Key: "updated_at", Value: now},
	}); err != nil {
		t.Fatalf("seed model %d: %v", id, err)
	}
}

func voiceRequest(t *testing.T, s *Server, method, target string, body []byte, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	mux.ServeHTTP(rec, req)
	return rec
}

func TestRestVoiceConfig_Disabled(t *testing.T) {
	s := testServerMongo(t)
	seedVoiceConfig(t, s, false, "", "", "")

	rec := voiceRequest(t, s, http.MethodGet, "/api/v1/user/voice/config?user_id=user-1", nil, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var view struct {
		Enabled bool   `json:"enabled"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Enabled || view.Reason != "disabled" {
		t.Fatalf("view = %+v, want the disabled reason", view)
	}
}

func TestRestVoiceConfig_Configured(t *testing.T) {
	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "3")
	seedProvider(t, s, 7, "platform", "LiteLLM", "http://llmhub:4000/v1", "sk-secret")
	seedChatModel(t, s, 3, 7, "Fast Chat", "gpt-4o-mini")

	rec := voiceRequest(t, s, http.MethodGet, "/api/v1/user/voice/config?user_id=user-1", nil, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "sk-secret") {
		t.Fatal("the config view must never carry a provider key")
	}
	var view struct {
		Enabled    bool   `json:"enabled"`
		Shortcut   string `json:"shortcut"`
		MaxSeconds int    `json:"max_seconds"`
		Language   string `json:"language"`
		Realtime   struct {
			Model        string `json:"model"`
			ProviderName string `json:"provider_name"`
			Available    bool   `json:"available"`
		} `json:"realtime"`
		Batch struct {
			Enabled   bool   `json:"enabled"`
			Trigger   string `json:"trigger"`
			Available bool   `json:"available"`
		} `json:"batch"`
		Polish struct {
			Enabled   bool   `json:"enabled"`
			ModelName string `json:"model_name"`
		} `json:"polish"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !view.Enabled || view.MaxSeconds != 90 || view.Language != "zh" {
		t.Fatalf("view = %+v", view)
	}
	if view.Shortcut != "Ctrl+Shift+M" {
		t.Fatalf("Shortcut = %q, want the configured value (the composer reads it from here)", view.Shortcut)
	}
	if view.Realtime.Model != "gpt-4o-transcribe" || view.Realtime.ProviderName != "LiteLLM" {
		t.Fatalf("realtime = %+v", view.Realtime)
	}
	if !view.Realtime.Available {
		t.Fatalf("the realtime tier must report itself as available: %+v", view.Realtime)
	}
	if view.Batch.Enabled || view.Batch.Available {
		t.Fatalf("the batch tier must read as off when it is not configured: %+v", view.Batch)
	}
	if !view.Polish.Enabled || view.Polish.ModelName != "Fast Chat" {
		t.Fatalf("polish = %+v", view.Polish)
	}
}

func TestRestVoiceSession_RequiresUserID(t *testing.T) {
	s := wireServer(t)

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/session", nil, "")

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRestVoiceSession_RefusesUnconfigured(t *testing.T) {
	s := testServerMongo(t)
	seedVoiceConfig(t, s, false, "", "", "")

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/session?user_id=user-1", nil, "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload.Code != "disabled" {
		t.Fatalf("code = %q, want disabled", payload.Code)
	}
}

func TestRestVoiceSession_RefusesUserScopedProvider(t *testing.T) {
	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "")
	seedProvider(t, s, 7, "user", "Mine", "http://llmhub:4000/v1", "sk-user")

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/session?user_id=user-1", nil, "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload.Code != "invalid_provider_scope" {
		t.Fatalf("code = %q, want invalid_provider_scope", payload.Code)
	}
}

func TestRestVoiceSession_MintsOneTimeID(t *testing.T) {
	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "")
	seedProvider(t, s, 7, "platform", "LiteLLM", "http://llmhub:4000/v1", "sk-1")

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/session?user_id=user-1", nil, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		SessionID  string `json:"session_id"`
		WSPath     string `json:"ws_path"`
		MaxSeconds int    `json:"max_seconds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.SessionID == "" || payload.WSPath != "/ws/voice" || payload.MaxSeconds != 90 {
		t.Fatalf("payload = %+v", payload)
	}
	if userID, ok := s.voiceSessions.Claim(payload.SessionID); !ok || userID != "user-1" {
		t.Fatalf("the minted id must be claimable by user-1, got (%q, %v)", userID, ok)
	}
	if _, ok := s.voiceSessions.Claim(payload.SessionID); ok {
		t.Fatal("the id must be single use")
	}
}

// TestRestVoicePolish_SendsExplicitNoneWhenOff pins the fix for a polish pass
// that kept thinking: "off" has to reach the upstream as an explicit none,
// because leaving the parameter out still lets a reasoning model think.
func TestRestVoicePolish_SendsExplicitNoneWhenOff(t *testing.T) {
	var got map[string]any
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "润色后的文本"}}},
		})
	}))
	defer chatSrv.Close()

	s := testServerMongo(t)
	// The fixture leaves polish.thinking out entirely, which is the state a
	// stored configuration is in before the key is ever edited.
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "3")
	seedChatModel(t, s, 3, 7, "Fast Chat", "gpt-4o-mini")
	seedProvider(t, s, 7, "platform", "LiteLLM", chatSrv.URL+"/v1", "sk-1")

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/polish?user_id=user-1",
		[]byte(`{"text":"原文文本"}`), "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got["reasoning_effort"] != voice.EffortNone {
		t.Fatalf("upstream reasoning_effort = %v, want %q", got["reasoning_effort"], voice.EffortNone)
	}
}

func TestRestVoicePolish_PassesTheThinkingEffort(t *testing.T) {
	var got map[string]any
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "润色后的文本"}}},
		})
	}))
	defer chatSrv.Close()

	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "3")
	if err := setConfigValue(t, s, voice.Component, "polish.thinking", "high"); err != nil {
		t.Fatalf("seed polish.thinking: %v", err)
	}
	seedChatModel(t, s, 3, 7, "Fast Chat", "gpt-4o-mini")
	seedProvider(t, s, 7, "platform", "LiteLLM", chatSrv.URL+"/v1", "sk-1")

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/polish?user_id=user-1",
		[]byte(`{"text":"原文文本"}`), "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got["reasoning_effort"] != "high" {
		t.Fatalf("upstream reasoning_effort = %v, want high", got["reasoning_effort"])
	}
}

func TestRestVoicePolish_DisabledReturnsTextUnchanged(t *testing.T) {
	// Nothing to seed: the polish path is disabled by configuration, so it never
	// reaches the provider or model lookups.
	s := testServerMongo(t)
	if err := seedConfig(t, s, voice.Component, []store.ConfigItem{
		{Key: "enabled", Value: "true", ValueType: "bool"},
		{Key: "polish.enabled", Value: "false", ValueType: "bool"},
	}); err != nil {
		t.Fatalf("seed voice config: %v", err)
	}

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/polish?user_id=user-1",
		[]byte(`{"text":"  原文文本  "}`), "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Text    string `json:"text"`
		Changed bool   `json:"changed"`
		Warning string `json:"warning"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload.Text != "原文文本" || payload.Changed || payload.Warning != "" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestRestVoicePolish_RewritesWithChatModel(t *testing.T) {
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "润色后的文本"}}},
		})
	}))
	defer chatSrv.Close()

	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "3")
	seedChatModel(t, s, 3, 7, "Fast Chat", "gpt-4o-mini")
	seedProvider(t, s, 7, "platform", "LiteLLM", chatSrv.URL+"/v1", "sk-1")

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/polish?user_id=user-1",
		[]byte(`{"text":"呃那个我想说"}`), "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Text    string `json:"text"`
		Changed bool   `json:"changed"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload.Text != "润色后的文本" || !payload.Changed {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestRestVoicePolish_KeepsTranscriptWhenProviderFails(t *testing.T) {
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer chatSrv.Close()

	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "3")
	seedChatModel(t, s, 3, 7, "Fast Chat", "gpt-4o-mini")
	seedProvider(t, s, 7, "platform", "LiteLLM", chatSrv.URL+"/v1", "sk-1")

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/polish?user_id=user-1",
		[]byte(`{"text":"原文文本"}`), "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Text    string `json:"text"`
		Changed bool   `json:"changed"`
		Warning string `json:"warning"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload.Text != "原文文本" || payload.Changed || payload.Warning != "polish_failed" {
		t.Fatalf("payload = %+v, want the transcript plus a polish_failed warning", payload)
	}
}

// TestRestVoicePolish_ShortTextSkipsTheModel pins the fix for a two-character
// transcript: a cleaning prompt answers such a fragment with a request for the
// text instead of the text, and that answer used to land in the composer as if
// it were the rewrite. Nothing is expected of the store or a provider here — the
// answer is the input.
func TestRestVoicePolish_ShortTextSkipsTheModel(t *testing.T) {
	s := testServerMongo(t)

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/polish?user_id=user-1",
		[]byte(`{"text":"好的"}`), "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Text    string `json:"text"`
		Changed bool   `json:"changed"`
		Warning string `json:"warning"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload.Text != "好的" || payload.Changed || payload.Warning != "" {
		t.Fatalf("payload = %+v, want the input back unchanged", payload)
	}
}

// TestRestVoicePolish_JustLongEnoughUsesTheModel is the other side of that
// threshold: four characters are still worth the rewrite pass.
func TestRestVoicePolish_JustLongEnoughUsesTheModel(t *testing.T) {
	var hit bool
	chatSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "我们走吧。"}}},
		})
	}))
	defer chatSrv.Close()

	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "3")
	seedChatModel(t, s, 3, 7, "Fast Chat", "gpt-4o-mini")
	seedProvider(t, s, 7, "platform", "LiteLLM", chatSrv.URL+"/v1", "sk-1")

	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/polish?user_id=user-1",
		[]byte(`{"text":"我们走吧"}`), "application/json")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Text    string `json:"text"`
		Changed bool   `json:"changed"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if !hit || payload.Text != "我们走吧。" || !payload.Changed {
		t.Fatalf("hit = %v, payload = %+v, want the rewrite", hit, payload)
	}
}

func TestHandleVoiceWS_RefusesUnknownSession(t *testing.T) {
	s := wireServer(t)

	req := httptest.NewRequest(http.MethodGet, "/ws/voice?session=bogus", nil)
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	mux.ServeHTTP(rec, req)

	if rec.Code == http.StatusSwitchingProtocols {
		t.Fatal("an unknown session id must never upgrade the connection")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestHandleVoiceWS_RelaysAudioAndEvents(t *testing.T) {
	// A stub provider that echoes how many audio bytes it received.
	received := make(chan int, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	providerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/realtime" || r.URL.Query().Get("intent") != "transcription" {
			t.Errorf("provider URL = %s", r.URL.String())
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var hello map[string]any
		if err := conn.ReadJSON(&hello); err != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"type": "session.updated"})
		total := 0
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg map[string]any
			_ = json.Unmarshal(raw, &msg)
			switch msg["type"] {
			case "input_audio_buffer.append":
				text, _ := msg["audio"].(string)
				total += len(text)
			case "input_audio_buffer.commit":
				received <- total
				// The provider streams the transcript as deltas and finishes the
				// utterance with a completion; both must reach the browser, in
				// order, through our relay.
				_ = conn.WriteJSON(map[string]any{
					"type": "conversation.item.input_audio_transcription.delta", "delta": "你",
				})
				_ = conn.WriteJSON(map[string]any{
					"type": "conversation.item.input_audio_transcription.delta", "delta": "好",
				})
				_ = conn.WriteJSON(map[string]any{
					"type": "conversation.item.input_audio_transcription.completed", "transcript": "你好",
				})
				return
			}
		}
	}))
	defer providerSrv.Close()

	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "")
	seedProvider(t, s, 7, "platform", "LiteLLM", providerSrv.URL+"/v1", "sk-1")

	mux := http.NewServeMux()
	s.registerRESTRoutes(mux)
	busSrv := httptest.NewServer(mux)
	defer busSrv.Close()

	id, err := s.voiceSessions.Mint("user-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(busSrv.URL, "http")+"/ws/voice?session="+id, nil)
	if err != nil {
		t.Fatalf("dial voice socket: %v", err)
	}
	defer client.Close()

	if err := client.WriteMessage(websocket.BinaryMessage, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if err := client.WriteMessage(websocket.BinaryMessage, []byte{5, 6, 7, 8}); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if err := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"commit"}`)); err != nil {
		t.Fatalf("write commit: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	var events []voice.Event
	for {
		_, raw, err := client.ReadMessage()
		if err != nil {
			t.Fatalf("read event: %v", err)
		}
		var event voice.Event
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		events = append(events, event)
		if event.Type == voice.EventDone {
			break
		}
	}
	want := []voice.Event{
		{Type: voice.EventDelta, Text: "你"},
		{Type: voice.EventDelta, Text: "好"},
		{Type: voice.EventDone, Text: "你好"},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %+v, want %d of them", events, len(want))
	}
	for i := range want {
		if events[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, events[i], want[i])
		}
	}

	select {
	case total := <-received:
		// Two 4-byte frames, base64 encoded one frame at a time: 8 characters
		// each.
		if total != 16 {
			t.Fatalf("provider received %d base64 chars, want 16", total)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the provider never saw the audio")
	}
}

func TestRestVoiceRefine_TranscribesWithTheBatchTier(t *testing.T) {
	var model, language, prompt string
	asrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse: %v", err)
		}
		model = r.FormValue("model")
		language = r.FormValue("language")
		prompt = r.FormValue("prompt")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "高精度文本"})
	}))
	defer asrSrv.Close()

	s := testServerMongo(t)
	seedVoiceConfigWithBatch(t, s, true, "7", "local-model", "", "9", "whisper-1", "always")
	seedProvider(t, s, 9, "platform", "LiteLLM", asrSrv.URL+"/v1", "sk-1")

	var body bytes.Buffer
	writer := newMultipart(t, &body, "audio", "take.wav", []byte("RIFFdata"))
	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/refine?user_id=user-1",
		body.Bytes(), writer.FormDataContentType())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Text         string `json:"text"`
		Model        string `json:"model"`
		ProviderName string `json:"provider_name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Text != "高精度文本" || payload.Model != "whisper-1" || payload.ProviderName != "LiteLLM" {
		t.Fatalf("payload = %+v", payload)
	}
	if model != "whisper-1" || language != "zh" || prompt != "术语" {
		t.Fatalf("the batch hints must reach the provider: model=%q language=%q prompt=%q", model, language, prompt)
	}
}

func TestRestVoiceRefine_NeedsTheBatchTier(t *testing.T) {
	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "local-model", "")

	var body bytes.Buffer
	writer := newMultipart(t, &body, "audio", "take.wav", []byte("RIFFdata"))
	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/refine?user_id=user-1",
		body.Bytes(), writer.FormDataContentType())

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "batch_disabled") {
		t.Fatalf("body = %s, want the batch_disabled code", rec.Body.String())
	}
}

// TestRestVoiceRefine_RefusesAUserScopeProvider is the security case: a
// user-owned provider must never serve a platform-wide pass.
func TestRestVoiceRefine_RefusesAUserScopeProvider(t *testing.T) {
	s := testServerMongo(t)
	seedVoiceConfigWithBatch(t, s, true, "7", "local-model", "", "9", "whisper-1", "always")
	seedProvider(t, s, 9, "user", "Mine", "http://llmhub:4000/v1", "sk-user")

	var body bytes.Buffer
	writer := newMultipart(t, &body, "audio", "take.wav", []byte("RIFFdata"))
	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/refine?user_id=user-1",
		body.Bytes(), writer.FormDataContentType())

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_provider_scope") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// newMultipart writes one file part so the transcription test can post what the
// admin card would post.
func newMultipart(t *testing.T, body *bytes.Buffer, field, filename string, content []byte) *multipart.Writer {
	t.Helper()
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile(field, filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return writer
}

func TestRestVoiceTranscribe_RequiresConfiguredVoice(t *testing.T) {
	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "")
	seedProvider(t, s, 7, "user", "Mine", "http://llmhub:4000/v1", "sk-user")

	var body bytes.Buffer
	writer := newMultipart(t, &body, "audio", "clip.wav", []byte("RIFF"))
	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/transcribe?user_id=user-1",
		body.Bytes(), writer.FormDataContentType())

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestRestVoiceTranscribe_PostsToProvider(t *testing.T) {
	asrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "测试文本"})
	}))
	defer asrSrv.Close()

	s := testServerMongo(t)
	seedVoiceConfig(t, s, true, "7", "gpt-4o-transcribe", "")
	seedProvider(t, s, 7, "platform", "LiteLLM", asrSrv.URL+"/v1", "sk-1")

	var body bytes.Buffer
	writer := newMultipart(t, &body, "audio", "clip.wav", []byte("RIFF"))
	rec := voiceRequest(t, s, http.MethodPost, "/api/v1/user/voice/transcribe?user_id=user-1",
		body.Bytes(), writer.FormDataContentType())

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload.Text != "测试文本" {
		t.Fatalf("payload = %+v", payload)
	}
}
