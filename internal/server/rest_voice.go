package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"gitee.com/kobeyoung81/nanobus/internal/voice"
	"github.com/gorilla/websocket"
)

// maxDiagnosticAudioBytes bounds the one-shot transcription upload used by the
// admin "test recognition" button.
const maxDiagnosticAudioBytes = 4 << 20

// maxRefineAudioBytes bounds the recording the composer uploads for the batch
// pass. At 16 kHz mono PCM16 a 120 s take is under 4 MB, so 8 MB leaves room for
// the WAV header and for a caller that records at 24 kHz.
const maxRefineAudioBytes = 8 << 20

// finalizeTimeout bounds the wait for the provider to finalize a committed
// utterance.
const finalizeTimeout = 15 * time.Second

// minPolishChars is the shortest transcript worth a chat-model rewrite. Below it
// there is no punctuation to add and no misheard word to correct, and a cleaning
// prompt tends to answer such a fragment with a request for the text instead of
// the text — which the caller would take for a rewrite and drop into the
// composer. The browser skips its own polish pass on the same threshold; a drift
// between the two only costs one call, never correctness.
const minPolishChars = 4

// voiceWSPath is the browser-facing dictation socket. It authenticates with a
// single-use id minted through the portal (see restVoiceSession), so it is the
// one endpoint the internal-key guard leaves open.
const voiceWSPath = "/ws/voice"

// errVoiceUnavailable carries the stable reason code the portal and the browser
// branch on (see voice.Config.Available).
type errVoiceUnavailable struct{ reason string }

func (e *errVoiceUnavailable) Error() string {
	switch e.reason {
	case "disabled":
		return "voice input is disabled"
	case "invalid_provider_scope":
		return "the configured voice provider is not a platform provider"
	case "provider_missing":
		return "the configured voice provider no longer exists"
	default:
		return "voice input is not configured"
	}
}

// voiceConfig loads the platform voice settings and reports whether dictation
// can run at all.
func (s *Server) voiceConfig() (voice.Config, error) {
	items, err := s.configItems(context.Background(), voice.Component)
	if err != nil {
		return voice.Config{}, fmt.Errorf("load voice config: %w", err)
	}
	cfg := voice.LoadConfig(items)
	if ok, reason := cfg.Available(); !ok {
		return cfg, &errVoiceUnavailable{reason: reason}
	}
	return cfg, nil
}

// resolvePlatformProvider looks one catalog provider up and refuses a user-scope
// entry: a user's own credentials must not serve the whole platform.
func (s *Server) resolvePlatformProvider(id int64) (voice.Provider, error) {
	p, err := s.store.GetModelProvider(id)
	if err != nil {
		return voice.Provider{}, fmt.Errorf("load voice provider: %w", err)
	}
	if p == nil {
		return voice.Provider{}, &errVoiceUnavailable{reason: "provider_missing"}
	}
	if p.Scope != "platform" {
		return voice.Provider{}, &errVoiceUnavailable{reason: "invalid_provider_scope"}
	}
	return voice.Provider{
		ID:      p.ID,
		Name:    p.Name,
		Kind:    p.Kind,
		BaseURL: p.BaseURL,
		APIKey:  p.APIKey,
	}, nil
}

// voiceProvider resolves the realtime tier's provider.
func (s *Server) voiceProvider() (voice.Config, voice.Provider, error) {
	cfg, err := s.voiceConfig()
	if err != nil {
		return cfg, voice.Provider{}, err
	}
	provider, err := s.resolvePlatformProvider(cfg.Realtime.ProviderID)
	return cfg, provider, err
}

// batchProvider resolves the batch tier's provider. It has its own reason code so
// the composer can tell "the pass is not configured" from "the pass failed".
func (s *Server) batchProvider() (voice.Config, voice.Provider, error) {
	var cfg voice.Config

	items, err := s.configItems(context.Background(), voice.Component)
	if err != nil {
		return cfg, voice.Provider{}, fmt.Errorf("load voice config: %w", err)
	}
	cfg = voice.LoadConfig(items)
	if !cfg.Enabled || !cfg.Batch.Available() {
		return cfg, voice.Provider{}, &errVoiceUnavailable{reason: "batch_disabled"}
	}
	provider, err := s.resolvePlatformProvider(cfg.Batch.ProviderID)
	return cfg, provider, err
}

// writeVoiceError maps the availability errors to HTTP responses; anything else
// is an internal error.
func writeVoiceError(w http.ResponseWriter, err error) {
	var unavailable *errVoiceUnavailable
	if errors.As(err, &unavailable) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": err.Error(),
			"code":  unavailable.reason,
		})
		return
	}
	slog.Error("voice: configuration lookup failed", "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

// restVoiceConfig reports what the browser needs to render the microphone: the
// trimmed platform settings, never the raw config items and never a key.
func (s *Server) restVoiceConfig(w http.ResponseWriter, r *http.Request) {
	if userIDFromQuery(r) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	items, err := s.configItems(context.Background(), voice.Component)
	if err != nil {
		slog.Error("voice: load config", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	cfg := voice.LoadConfig(items)
	available, reason := cfg.Available()

	// Names are best-effort: an unset or dangling id must not break the view.
	realtimeName := ""
	if cfg.Realtime.ProviderID != 0 {
		if p, err := s.store.GetModelProvider(cfg.Realtime.ProviderID); err == nil && p != nil {
			realtimeName = p.Name
		}
	}
	batchName := ""
	if cfg.Batch.ProviderID != 0 {
		if p, err := s.store.GetModelProvider(cfg.Batch.ProviderID); err == nil && p != nil {
			batchName = p.Name
		}
	}
	polishModelName := ""
	if cfg.Polish.ModelID != 0 {
		if m, err := s.store.GetModel(cfg.Polish.ModelID); err == nil && m != nil {
			polishModelName = m.Name
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":     available,
		"reason":      reason,
		"shortcut":    cfg.Shortcut,
		"max_seconds": cfg.Realtime.MaxSeconds,
		"language":    cfg.Realtime.Language,
		"realtime": map[string]any{
			"model":         cfg.Realtime.Model,
			"provider_name": realtimeName,
			"available":     cfg.Realtime.Available(),
			"audio_rate":    voice.AudioRate,
		},
		"batch": map[string]any{
			"enabled":       cfg.Batch.Enabled,
			"trigger":       cfg.Batch.Trigger,
			"model":         cfg.Batch.Model,
			"provider_name": batchName,
			"available":     cfg.Batch.Available(),
		},
		"polish": map[string]any{
			"enabled":          cfg.Polish.Enabled && cfg.Polish.ModelID != 0,
			"model_name":       polishModelName,
			"skip_after_batch": cfg.Polish.SkipAfterBatch,
		},
	})
}

// restVoiceSession mints the one-time id that opens the voice WebSocket.
func (s *Server) restVoiceSession(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	cfg, _, err := s.voiceProvider()
	if err != nil {
		writeVoiceError(w, err)
		return
	}

	id, err := s.voiceSessions.Mint(userID)
	if err != nil {
		slog.Error("voice: mint session", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id":  id,
		"expires_in":  int(voiceSessionTTL.Seconds()),
		"ws_path":     voiceWSPath,
		"max_seconds": cfg.Realtime.MaxSeconds,
		// The session is the authority on the capture format: the browser mints it
		// before it opens the microphone, so this is where it learns the rate the
		// provider expects.
		"audio_rate": voice.AudioRate,
	})
}

// restVoicePolish rewrites a finished transcript with the configured chat
// model. It never fails the request when polishing is off or the model
// misbehaves: the caller keeps the transcript, and the response says so.
func (s *Server) restVoicePolish(w http.ResponseWriter, r *http.Request) {
	if userIDFromQuery(r) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "text is required"})
		return
	}

	respond := func(out string, changed bool, warning string) {
		payload := map[string]any{"text": out, "changed": changed}
		if warning != "" {
			payload["warning"] = warning
		}
		writeJSON(w, http.StatusOK, payload)
	}

	// Too short to be worth a rewrite. The platform settings are not even read:
	// the answer is the input, the same one a disabled polish pass gives.
	if utf8.RuneCountInString(text) < minPolishChars {
		respond(text, false, "")
		return
	}

	items, err := s.configItems(context.Background(), voice.Component)
	if err != nil {
		slog.Error("voice: load config", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	cfg := voice.LoadConfig(items)

	if !cfg.Polish.Enabled || cfg.Polish.ModelID == 0 {
		respond(text, false, "")
		return
	}
	modelName, provider, err := s.chatModel(cfg.Polish.ModelID)
	if err != nil {
		slog.Warn("voice: polish model unavailable", "error", err)
		respond(text, false, "polish_unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), voice.CallTimeout)
	defer cancel()
	out, err := voice.Polish(ctx, provider, modelName, cfg.Polish.Prompt, text, cfg.Polish.Thinking)
	if err != nil {
		slog.Warn("voice: polish failed", "error", err)
		respond(text, false, "polish_failed")
		return
	}
	if out == text {
		respond(text, false, "")
		return
	}
	respond(out, true, "")
}

// chatModel resolves a catalog model id to its provider API model name and// connection, refusing a user-scope provider for the same reason voiceProvider
// does.
func (s *Server) chatModel(modelID int64) (string, voice.Provider, error) {
	m, err := s.store.GetModel(modelID)
	if err != nil {
		return "", voice.Provider{}, fmt.Errorf("load model: %w", err)
	}
	if m == nil {
		return "", voice.Provider{}, fmt.Errorf("model %d not found", modelID)
	}
	p, err := s.store.GetModelProvider(m.ProviderID)
	if err != nil {
		return "", voice.Provider{}, fmt.Errorf("load provider: %w", err)
	}
	if p == nil {
		return "", voice.Provider{}, fmt.Errorf("provider %d not found", m.ProviderID)
	}
	if p.Scope != "platform" {
		return "", voice.Provider{}, fmt.Errorf("provider %d is not a platform provider", p.ID)
	}
	return m.Model, voice.Provider{
		ID:      p.ID,
		Name:    p.Name,
		Kind:    p.Kind,
		BaseURL: p.BaseURL,
		APIKey:  p.APIKey,
	}, nil
}

// transcriptionTarget resolves what one one-shot transcription runs against: the
// realtime tier by default, the batch tier when the caller asks for it with
// target=batch.
func (s *Server) transcriptionTarget(target string) (transcribeTarget, error) {
	if strings.EqualFold(strings.TrimSpace(target), "batch") {
		cfg, provider, err := s.batchProvider()
		if err != nil {
			return transcribeTarget{}, err
		}
		return transcribeTarget{provider: provider, model: cfg.Batch.Model, language: cfg.Batch.Language, prompt: cfg.Batch.Prompt}, nil
	}
	cfg, provider, err := s.voiceProvider()
	if err != nil {
		return transcribeTarget{}, err
	}
	return transcribeTarget{provider: provider, model: cfg.Realtime.Model, language: cfg.Realtime.Language, prompt: cfg.Realtime.Prompt}, nil
}

// transcribeTarget is one resolved one-shot transcription target.
type transcribeTarget struct {
	provider voice.Provider
	model    string
	language string
	prompt   string
}

// restVoiceTranscribe is the one-shot transcription behind the admin test
// button (and available as a REST fallback later). The live dictation path is
// the WebSocket below.
//
// target picks the tier it exercises — the realtime tier by default, the batch
// tier with target=batch — so one button can test both.
func (s *Server) restVoiceTranscribe(w http.ResponseWriter, r *http.Request) {
	if userIDFromQuery(r) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDiagnosticAudioBytes)
	if err := r.ParseMultipartForm(maxDiagnosticAudioBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected a multipart audio upload"})
		return
	}
	target, err := s.transcriptionTarget(r.FormValue("target"))
	if err != nil {
		writeVoiceError(w, err)
		return
	}
	file, header, err := r.FormFile("audio")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "audio field is required"})
		return
	}
	defer file.Close()
	audio, err := io.ReadAll(io.LimitReader(file, maxDiagnosticAudioBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read audio"})
		return
	}
	if len(audio) > maxDiagnosticAudioBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "audio exceeds max size"})
		return
	}
	filename := "clip.wav"
	if header != nil && header.Filename != "" {
		filename = header.Filename
	}

	ctx, cancel := context.WithTimeout(r.Context(), voice.CallTimeout)
	defer cancel()
	text, err := voice.TranscribeFile(ctx, target.provider, target.model, target.language, target.prompt, filename, audio)
	if err != nil {
		slog.Warn("voice: diagnostic transcription failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"text": text})
}

// restVoiceRefine re-transcribes a finished recording with the batch tier's
// model — the second, higher-accuracy pass the composer runs when a take ends.
// It answers with the transcript alone: replacing the text and running the
// polish pass are the composer's decisions, and keeping them there is what makes
// the three tiers visible in the UI.
func (s *Server) restVoiceRefine(w http.ResponseWriter, r *http.Request) {
	if userIDFromQuery(r) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	cfg, provider, err := s.batchProvider()
	if err != nil {
		writeVoiceError(w, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRefineAudioBytes)
	if err := r.ParseMultipartForm(maxRefineAudioBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected a multipart audio upload"})
		return
	}
	file, _, err := r.FormFile("audio")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "audio field is required"})
		return
	}
	defer file.Close()
	audio, err := io.ReadAll(io.LimitReader(file, maxRefineAudioBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "failed to read audio"})
		return
	}
	if len(audio) > maxRefineAudioBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "audio exceeds max size"})
		return
	}
	if len(audio) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the recording is empty"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), voice.CallTimeout)
	defer cancel()
	text, err := voice.TranscribeFile(ctx, provider, cfg.Batch.Model, cfg.Batch.Language, cfg.Batch.Prompt, "take.wav", audio)
	if err != nil {
		slog.Warn("voice: batch transcription failed", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"text":          text,
		"model":         cfg.Batch.Model,
		"provider_name": provider.Name,
	})
}

// handleVoiceWS proxies one browser dictation session to the provider's
// realtime transcription session, turning raw PCM frames from the browser into
// input_audio_buffer events.
func (s *Server) handleVoiceWS(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.voiceSessions.Claim(r.URL.Query().Get("session")); !ok {
		http.Error(w, "unknown or expired voice session", http.StatusForbidden)
		return
	}
	cfg, provider, err := s.voiceProvider()
	if err != nil {
		var unavailable *errVoiceUnavailable
		if errors.As(err, &unavailable) {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		slog.Error("voice: configuration lookup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	wsURL, err := voice.RealtimeURL(provider.BaseURL, cfg.Realtime.Model)
	if err != nil {
		slog.Error("voice: build realtime URL", "error", err)
		http.Error(w, "voice provider is misconfigured", http.StatusServiceUnavailable)
		return
	}

	// Dial upstream before upgrading the browser connection: a provider that
	// refuses the session should surface as an HTTP error, not as a socket that
	// silently closes.
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.Realtime.MaxSeconds)*time.Second+finalizeTimeout)
	defer cancel()
	upstream, err := voice.Dial(ctx, wsURL, provider.APIKey, voice.Settings{
		Model:    cfg.Realtime.Model,
		Language: cfg.Realtime.Language,
		Prompt:   cfg.Realtime.Prompt,
		Rate:     voice.AudioRate,
	})
	if err != nil {
		slog.Warn("voice: realtime session refused", "error", err)
		http.Error(w, "voice provider rejected the session", http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	client, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Debug("voice: upgrade failed", "error", err)
		return
	}
	defer client.Close()

	committed := make(chan struct{}, 1)
	clientGone := make(chan struct{})
	var pushed atomic.Int64
	go func() {
		defer close(clientGone)
		pumpBrowserAudio(ctx, client, upstream, cfg.Realtime.CommitIntervalMS, committed, &pushed)
	}()

	started := time.Now()
	events := upstream.Events(ctx)
	var finalize <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			_ = client.WriteMessage(websocket.TextMessage, mustJSON(voice.Event{
				Type: voice.EventError, Message: "dictation timed out",
			}))
			return
		case <-clientGone:
			slog.Info("voice: browser closed the dictation",
				"seconds", int(time.Since(started).Seconds()),
				"audio_bytes", pushed.Load(),
			)
			return
		case <-committed:
			finalize = time.After(finalizeTimeout)
		case <-finalize:
			_ = client.WriteMessage(websocket.TextMessage, mustJSON(voice.Event{
				Type: voice.EventError, Message: "the provider did not finish the transcript",
			}))
			return
		case event, ok := <-events:
			if !ok {
				// The provider ended the stream (it also reports why through an
				// error event just before this); log it so the next report of
				// "dictation cut off" has an answer instead of a guess.
				slog.Info("voice: provider stream ended",
					"seconds", int(time.Since(started).Seconds()),
					"audio_bytes", pushed.Load(),
				)
				return
			}
			switch event.Type {
			case voice.EventWarning:
				slog.Info("voice: provider warning", "message", event.Message, "audio_bytes", pushed.Load())
			case voice.EventError:
				slog.Warn("voice: provider error",
					"message", event.Message,
					"seconds", int(time.Since(started).Seconds()),
					"audio_bytes", pushed.Load(),
				)
			case voice.EventDone:
				slog.Info("voice: transcript complete",
					"seconds", int(time.Since(started).Seconds()),
					"audio_bytes", pushed.Load(),
					"chars", len(event.Text),
				)
			}
			if err := client.WriteMessage(websocket.TextMessage, mustJSON(event)); err != nil {
				return
			}
			if event.Type == voice.EventDone || event.Type == voice.EventError {
				return
			}
		}
	}
}

// pumpBrowserAudio feeds the provider: binary frames are PCM16 at the provider's
// rate, text frames are control messages ("commit" / "close").
func pumpBrowserAudio(
	ctx context.Context,
	client *websocket.Conn,
	upstream *voice.Session,
	commitIntervalMS int,
	committed chan<- struct{},
	pushed *atomic.Int64,
) {
	// 24000 samples per second, 2 bytes per sample.
	const bytesPerMS = voice.AudioRate * 2 / 1000
	sinceCommit := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		messageType, payload, err := client.ReadMessage()
		if err != nil {
			return
		}
		switch messageType {
		case websocket.BinaryMessage:
			if err := upstream.Append(payload); err != nil {
				slog.Warn("voice: provider append failed", "error", err, "audio_bytes", pushed.Load())
				return
			}
			pushed.Add(int64(len(payload)))
			if commitIntervalMS > 0 {
				sinceCommit += len(payload)
				if sinceCommit >= commitIntervalMS*bytesPerMS {
					sinceCommit = 0
					_ = upstream.Commit()
				}
			}
		case websocket.TextMessage:
			var control struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(payload, &control); err != nil {
				continue
			}
			switch control.Type {
			case "commit":
				if err := upstream.Commit(); err != nil {
					return
				}
				select {
				case committed <- struct{}{}:
				default:
				}
			case "close":
				return
			}
		}
	}
}

func mustJSON(v any) []byte {
	payload, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"type":"error","message":"internal error"}`)
	}
	return payload
}
