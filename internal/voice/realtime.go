package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Event kinds the relay forwards to the browser. The provider's own event names
// vary, but the shape is the OpenAI one: text arrives as deltas, and an
// utterance ends with a completion.
const (
	EventDelta = "delta"
	EventDone  = "done"
	EventError = "error"
	// EventWarning carries something the browser should surface but continue
	// past (see commitNoise).
	EventWarning = "warning"
)

// Event is one normalized realtime event. The provider's raw event names differ
// between the GA and beta surfaces (and between transcription models), so they
// are folded into these three kinds here, once, instead of in the relay.
type Event struct {
	Type    string `json:"type"`
	Text    string `json:"text,omitempty"`
	Message string `json:"message,omitempty"`
}

// Settings configure one transcription session.
type Settings struct {
	Model    string
	Language string
	Prompt   string
	// Rate is the capture rate the session declares; 0 means the default the
	// realtime API takes (AudioRate).
	Rate int
}

// sessionUpdate is the GA transcription-session configuration. turn_detection
// stays null: "hold to talk" is a manual turn, so the client decides when the
// utterance ends (see Commit).
type sessionUpdate struct {
	Type    string `json:"type"`
	Session struct {
		Type  string `json:"type"`
		Audio struct {
			Input struct {
				Format struct {
					Type string `json:"type"`
					Rate int    `json:"rate"`
				} `json:"format"`
				Transcription struct {
					Model    string `json:"model"`
					Language string `json:"language,omitempty"`
					Prompt   string `json:"prompt,omitempty"`
				} `json:"transcription"`
				TurnDetection any `json:"turn_detection"`
			} `json:"input"`
		} `json:"audio"`
	} `json:"session"`
}

// Session is a live transcription session at the provider.
type Session struct {
	conn        *websocket.Conn
	writeMu     sync.Mutex
	closeOnce   sync.Once
	sessionDone chan struct{}
}

// sessionReadyTimeout bounds the wait for the provider to confirm the session
// configuration before any audio is sent.
const sessionReadyTimeout = 10 * time.Second

// Dial opens the provider's realtime session and waits for session.updated.
// It reads from the connection during the handshake, so Events must not be
// called until Dial returns.
func Dial(ctx context.Context, wsURL, apiKey string, set Settings) (*Session, error) {
	header := http.Header{}
	if apiKey != "" {
		header.Set("Authorization", "Bearer "+apiKey)
	}

	dialer := websocket.Dialer{HandshakeTimeout: sessionReadyTimeout}
	conn, resp, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("realtime dial failed: HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("realtime dial failed: %w", err)
	}

	session := &Session{conn: conn, sessionDone: make(chan struct{})}

	update := sessionUpdate{Type: "session.update"}
	update.Session.Type = "transcription"
	update.Session.Audio.Input.Format.Type = "audio/pcm"
	rate := set.Rate
	if rate <= 0 {
		rate = AudioRate
	}
	update.Session.Audio.Input.Format.Rate = rate
	update.Session.Audio.Input.Transcription.Model = set.Model
	update.Session.Audio.Input.Transcription.Language = set.Language
	update.Session.Audio.Input.Transcription.Prompt = set.Prompt
	update.Session.Audio.Input.TurnDetection = nil

	if err := session.writeJSON(update); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("send session.update: %w", err)
	}

	if err := session.awaitReady(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return session, nil
}

// awaitReady reads until the provider confirms the session, or reports why it
// refused it.
func (s *Session) awaitReady() error {
	_ = s.conn.SetReadDeadline(time.Now().Add(sessionReadyTimeout))
	defer func() { _ = s.conn.SetReadDeadline(time.Time{}) }()

	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			return fmt.Errorf("realtime session not confirmed: %w", err)
		}
		// The confirmation is checked first: normalizeEvent deliberately drops
		// events the browser does not care about, and session.updated is one of
		// them.
		if strings.HasSuffix(eventTypeName(raw), "session.updated") {
			return nil
		}
		if event, ok := normalizeEvent(raw); ok && event.Type == EventError {
			return fmt.Errorf("realtime session rejected: %s", event.Message)
		}
	}
}

// Append sends one PCM16 chunk of audio.
func (s *Session) Append(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	return s.writeJSON(map[string]string{
		"type":  "input_audio_buffer.append",
		"audio": base64.StdEncoding.EncodeToString(pcm),
	})
}

// Commit closes the current utterance so the provider finalizes its transcript.
func (s *Session) Commit() error {
	return s.writeJSON(map[string]string{"type": "input_audio_buffer.commit"})
}

// Events returns the normalized event stream. The channel closes when the
// provider connection ends or the context is cancelled.
func (s *Session) Events(ctx context.Context) <-chan Event {
	out := make(chan Event, 8)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_, raw, err := s.conn.ReadMessage()
			if err != nil {
				select {
				case <-s.sessionDone:
					return // closed by us; not an error
				default:
				}
				select {
				case out <- Event{Type: EventError, Message: "realtime connection ended: " + err.Error()}:
				case <-ctx.Done():
				}
				return
			}
			event, ok := normalizeEvent(raw)
			if !ok {
				continue
			}
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// Close ends the session.
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		close(s.sessionDone)
		err = s.conn.Close()
	})
	return err
}

func (s *Session) writeJSON(v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.conn.WriteMessage(websocket.TextMessage, payload)
}

// eventTypeName returns the raw event type, used for handshake matching.
func eventTypeName(raw []byte) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return ""
	}
	return envelope.Type
}

// normalizeEvent folds a provider event into one of the three kinds we forward.
// It accepts both the GA ("…input_audio_transcription.delta/completed") and the
// older beta ("…audio_transcript.delta/done") names, and reports false for the
// many events the browser does not care about.
func normalizeEvent(raw []byte) (Event, bool) {
	var payload struct {
		Type       string `json:"type"`
		Delta      string `json:"delta"`
		Transcript string `json:"transcript"`
		Text       string `json:"text"`
		Message    string `json:"message"`
		Error      *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		slog.Debug("voice: undecodable provider event", "error", err)
		return Event{}, false
	}

	switch {
	case strings.HasSuffix(payload.Type, "audio_transcription.delta"),
		strings.HasSuffix(payload.Type, "audio_transcript.delta"):
		return Event{Type: EventDelta, Text: payload.Delta}, true
	case strings.HasSuffix(payload.Type, "audio_transcription.completed"),
		strings.HasSuffix(payload.Type, "audio_transcription.done"),
		strings.HasSuffix(payload.Type, "audio_transcript.done"):
		text := payload.Transcript
		if text == "" {
			text = payload.Text
		}
		return Event{Type: EventDone, Text: text}, true
	case payload.Type == "error":
		message := payload.Message
		if payload.Error != nil && payload.Error.Message != "" {
			message = payload.Error.Message
		}
		if message == "" {
			message = "provider error"
		}
		if commitNoise(message) {
			// The provider commits utterances on its own (its VAD sees the
			// speech), so our release commit is redundant and gets rejected
			// when there is nothing left to commit. That is not a failure —
			// treating it as one is what skipped the polish pass.
			return Event{Type: EventWarning, Message: message}, true
		}
		return Event{Type: EventError, Message: message}, true
	}
	return Event{}, false
}

// commitNoise reports whether an error is the harmless "you asked me to commit
// a buffer that is already dealt with" complaint, which providers word
// differently.
func commitNoise(message string) bool {
	m := strings.ToLower(message)
	if strings.Contains(m, "no invalid audio stream") {
		return true
	}
	if !strings.Contains(m, "commit") {
		return false
	}
	for _, word := range []string{"empty", "no audio", "too small", "buffer is empty"} {
		if strings.Contains(m, word) {
			return true
		}
	}
	return false
}
