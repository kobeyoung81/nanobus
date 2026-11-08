package voice

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTranscribeFilePostsMultipart(t *testing.T) {
	var model string
	var filename string
	var payload []byte
	var auth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		auth = r.Header.Get("Authorization")
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content type: %v", err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("read part: %v", err)
				break
			}
			body, _ := io.ReadAll(part)
			switch part.FormName() {
			case "model":
				model = string(body)
			case "file":
				filename = part.FileName()
				payload = body
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": " 你好世界 "})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	text, err := TranscribeFile(ctx, Provider{BaseURL: srv.URL + "/v1", APIKey: "sk-1"}, "gpt-4o-transcribe", "", "", "clip.wav", []byte("RIFFdata"))
	if err != nil {
		t.Fatalf("TranscribeFile: %v", err)
	}
	if text != "你好世界" {
		t.Errorf("text = %q, want the trimmed transcript", text)
	}
	if model != "gpt-4o-transcribe" || filename != "clip.wav" || string(payload) != "RIFFdata" {
		t.Errorf("upstream got model=%q file=%q payload=%q", model, filename, payload)
	}
	if auth != "Bearer sk-1" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestTranscribeFileReportsUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"model not found"}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := TranscribeFile(ctx, Provider{BaseURL: srv.URL + "/v1"}, "nope", "", "", "clip.wav", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v, want an HTTP 404 error", err)
	}
}

func TestPolishReadsStringContent(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": " 润色后的文本 "}},
			},
		})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := Polish(ctx, Provider{BaseURL: srv.URL + "/v1"}, "gpt-4o-mini", "整理一下", "呃那个我想说", "")
	if err != nil {
		t.Fatalf("Polish: %v", err)
	}
	if out != "润色后的文本" {
		t.Errorf("out = %q", out)
	}
	messages, _ := got["messages"].([]any)
	if len(messages) != 2 {
		t.Fatalf("messages = %v, want system + user", got["messages"])
	}
	system, _ := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "整理一下" {
		t.Errorf("system message = %v", system)
	}
	if got["model"] != "gpt-4o-mini" {
		t.Errorf("model = %v", got["model"])
	}
}

func TestPolishReadsContentParts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":[{"type":"text","text":"甲"},{"type":"text","text":"乙"}]}}]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := Polish(ctx, Provider{BaseURL: srv.URL + "/v1"}, "m", "p", "原文", "")
	if err != nil {
		t.Fatalf("Polish: %v", err)
	}
	if out != "甲乙" {
		t.Errorf("out = %q, want the concatenated parts", out)
	}
}

func TestPolishRejectsEmptyContentAndMissingModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"thinking"}}]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Polish(ctx, Provider{BaseURL: srv.URL + "/v1"}, "m", "p", "原文", ""); err == nil {
		t.Error("a reply with no text must be an error so the caller keeps the transcript")
	}
	if _, err := Polish(ctx, Provider{BaseURL: srv.URL + "/v1"}, "  ", "p", "原文", ""); err == nil {
		t.Error("an empty model must be an error")
	}
}

func TestPolishSendsReasoningEffortOnlyWhenSet(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"润色"}}]}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := Provider{BaseURL: srv.URL + "/v1"}

	if _, err := Polish(ctx, provider, "m", "p", "原文", "low"); err != nil {
		t.Fatalf("Polish: %v", err)
	}
	if got["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort = %v, want low", got["reasoning_effort"])
	}

	if _, err := Polish(ctx, provider, "m", "p", "原文", ""); err != nil {
		t.Fatalf("Polish: %v", err)
	}
	// Off means the key is absent, not empty: a model that does not reason
	// rejects an empty effort.
	if value, present := got["reasoning_effort"]; present {
		t.Fatalf("reasoning_effort must not be sent when off, got %v", value)
	}
}
