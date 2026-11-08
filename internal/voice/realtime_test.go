package voice

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// stubWS starts a provider stub: it upgrades the connection, reports the first
// client message on firstCh and the Authorization header on authCh, then runs
// the caller's script.
func stubWS(
	t *testing.T,
	script func(conn *websocket.Conn, first map[string]any, send func(v any)),
	firstCh chan map[string]any,
	authCh chan string,
) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authCh != nil {
			authCh <- r.Header.Get("Authorization")
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		var first map[string]any
		if err := conn.ReadJSON(&first); err != nil {
			return
		}
		if firstCh != nil {
			firstCh <- first
		}
		send := func(v any) { _ = conn.WriteJSON(v) }
		script(conn, first, send)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsURL(srv *httptest.Server) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func TestDialSendsGASSessionUpdate(t *testing.T) {
	firstCh := make(chan map[string]any, 1)
	authCh := make(chan string, 1)
	srv := stubWS(t, func(conn *websocket.Conn, _ map[string]any, send func(any)) {
		send(map[string]any{"type": "session.updated"})
		<-time.After(2 * time.Second)
	}, firstCh, authCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := Dial(ctx, wsURL(srv), "sk-test", Settings{
		Model:    "gpt-4o-transcribe",
		Language: "zh",
		Prompt:   "术语：myriadmind",
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer session.Close()

	if got := <-authCh; got != "Bearer sk-test" {
		t.Errorf("Authorization = %q, want the provider key", got)
	}

	first := <-firstCh
	if first["type"] != "session.update" {
		t.Fatalf("first event = %v, want session.update", first["type"])
	}
	session0, ok := first["session"].(map[string]any)
	if !ok {
		t.Fatalf("session.update has no session object: %v", first)
	}
	if session0["type"] != "transcription" {
		t.Errorf("session.type = %v, want transcription", session0["type"])
	}
	input := session0["audio"].(map[string]any)["input"].(map[string]any)
	format := input["format"].(map[string]any)
	if format["type"] != "audio/pcm" || format["rate"].(float64) != AudioRate {
		t.Errorf("input format = %v, want audio/pcm @ %d", format, AudioRate)
	}
	if input["turn_detection"] != nil {
		t.Errorf("turn_detection = %v, want null (manual commit)", input["turn_detection"])
	}
	transcription := input["transcription"].(map[string]any)
	if transcription["model"] != "gpt-4o-transcribe" || transcription["language"] != "zh" || transcription["prompt"] != "术语：myriadmind" {
		t.Errorf("transcription = %v", transcription)
	}
}

func TestDialReportsRejectedSession(t *testing.T) {
	srv := stubWS(t, func(conn *websocket.Conn, _ map[string]any, send func(any)) {
		send(map[string]any{"type": "error", "error": map[string]any{"message": "model not found"}})
	}, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := Dial(ctx, wsURL(srv), "", Settings{Model: "nope"})
	if err == nil {
		session.Close()
		t.Fatal("Dial must fail when the provider rejects the session")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Errorf("error = %v, want the provider message", err)
	}
}

func TestSessionStreamsAudioAndEvents(t *testing.T) {
	audioCh := make(chan []byte, 4)
	srv := stubWS(t, func(conn *websocket.Conn, _ map[string]any, send func(any)) {
		send(map[string]any{"type": "session.updated"})
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			switch msg["type"] {
			case "input_audio_buffer.append":
				encoded, _ := msg["audio"].(string)
				pcm, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil {
					return
				}
				audioCh <- pcm
			case "input_audio_buffer.commit":
				// A realistic stream: deltas while finalizing, then completion.
				send(map[string]any{"type": "conversation.item.input_audio_transcription.delta", "delta": "你"})
				send(map[string]any{"type": "conversation.item.input_audio_transcription.delta", "delta": "好"})
				send(map[string]any{"type": "conversation.item.input_audio_transcription.completed", "transcript": "你好"})
			}
		}
	}, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := Dial(ctx, wsURL(srv), "", Settings{Model: "gpt-4o-transcribe"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer session.Close()

	if err := session.Append([]byte{1, 2, 3}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := session.Append([]byte{4, 5}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	first := <-audioCh
	second := <-audioCh
	if string(first) != string([]byte{1, 2, 3}) || string(second) != string([]byte{4, 5}) {
		t.Fatalf("provider received %v then %v", first, second)
	}

	events := session.Events(ctx)
	if err := session.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	var got []Event
	for len(got) < 3 {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("event stream closed early, got %+v", got)
			}
			got = append(got, event)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for events, got %+v", got)
		}
	}
	want := []Event{
		{Type: EventDelta, Text: "你"},
		{Type: EventDelta, Text: "好"},
		{Type: EventDone, Text: "你好"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestEventStreamReportsProviderError(t *testing.T) {
	srv := stubWS(t, func(conn *websocket.Conn, _ map[string]any, send func(any)) {
		send(map[string]any{"type": "session.updated"})
		send(map[string]any{"type": "error", "message": "rate limited"})
	}, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, err := Dial(ctx, wsURL(srv), "", Settings{Model: "m"})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer session.Close()

	select {
	case event := <-session.Events(ctx):
		if event.Type != EventError || event.Message != "rate limited" {
			t.Fatalf("event = %+v, want an error event", event)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the error event")
	}
}

func TestNormalizeEvent(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want Event
		ok   bool
	}{
		{
			name: "GA delta",
			raw:  `{"type":"conversation.item.input_audio_transcription.delta","delta":"你好"}`,
			want: Event{Type: EventDelta, Text: "你好"},
			ok:   true,
		},
		{
			name: "beta delta",
			raw:  `{"type":"conversation.item.audio_transcript.delta","delta":"hi"}`,
			want: Event{Type: EventDelta, Text: "hi"},
			ok:   true,
		},
		{
			name: "GA completed",
			raw:  `{"type":"conversation.item.input_audio_transcription.completed","transcript":"整段"}`,
			want: Event{Type: EventDone, Text: "整段"},
			ok:   true,
		},
		{
			name: "beta done falls back to text",
			raw:  `{"type":"conversation.item.audio_transcript.done","text":"整段"}`,
			want: Event{Type: EventDone, Text: "整段"},
			ok:   true,
		},
		{
			name: "committing an already-dealt-with buffer is a warning, not an error",
			raw:  `{"type":"error","error":{"type":"invalid_request_error","message":"Error committing input audio buffer, maybe no invalid audio stream."}}`,
			want: Event{Type: EventWarning, Message: "Error committing input audio buffer, maybe no invalid audio stream."},
			ok:   true,
		},
		{
			name: "error with nested message",
			raw:  `{"type":"error","error":{"message":"boom"}}`,
			want: Event{Type: EventError, Message: "boom"},
			ok:   true,
		},
		{
			name: "error without a message",
			raw:  `{"type":"error"}`,
			want: Event{Type: EventError, Message: "provider error"},
			ok:   true,
		},
		{name: "session.updated is not forwarded", raw: `{"type":"session.updated"}`, ok: false},
		{name: "unknown event", raw: `{"type":"response.output_item.added"}`, ok: false},
		{name: "not json", raw: `nonsense`, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := normalizeEvent([]byte(tc.raw))
			if ok != tc.ok {
				t.Fatalf("normalizeEvent ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("normalizeEvent = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCommitNoiseIsNotAnError(t *testing.T) {
	cases := []struct {
		message string
		want    bool
	}{
		{"Error committing input audio buffer, maybe no invalid audio stream.", true},
		{"Error committing input audio buffer: buffer is empty", true},
		{"commit failed: no audio to commit", true},
		{"rate limited", false},
		{"model not found", false},
		{"Error committing input audio buffer: upstream timed out", false},
	}
	for _, tc := range cases {
		if got := commitNoise(tc.message); got != tc.want {
			t.Errorf("commitNoise(%q) = %v, want %v", tc.message, got, tc.want)
		}
	}
}
