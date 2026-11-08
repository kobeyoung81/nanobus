package llmprobe

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeProviderListsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %s, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer k" {
			t.Errorf("auth = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}, {"id": "m2"}}})
	}))
	defer srv.Close()

	ids, err := ProbeProvider(context.Background(), "openai", srv.URL, "k")
	if err != nil {
		t.Fatalf("ProbeProvider: %v", err)
	}
	if len(ids) != 2 || ids[0].ID != "m1" || ids[1].ID != "m2" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestProbeProviderParsesMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "plain"},
			{"id": "ctx", "context_length": 128000, "max_output_tokens": 4096},
			{"id": "google", "input_token_limit": 32000, "output_token_limit": 8192},
			{"id": "openrouter", "top_provider": map[string]any{"context_length": 200000, "max_completion_tokens": 10000}},
		}})
	}))
	defer srv.Close()

	models, err := ProbeProvider(context.Background(), "openai", srv.URL, "k")
	if err != nil {
		t.Fatalf("ProbeProvider: %v", err)
	}
	if len(models) != 4 {
		t.Fatalf("models = %v", models)
	}
	if models[0].ContextWindow != 0 || models[0].MaxOutputTokens != 0 {
		t.Fatalf("plain = %+v", models[0])
	}
	if models[1].ContextWindow != 128000 || models[1].MaxOutputTokens != 4096 {
		t.Fatalf("ctx = %+v", models[1])
	}
	if models[2].ContextWindow != 32000 || models[2].MaxOutputTokens != 8192 {
		t.Fatalf("google = %+v", models[2])
	}
	if models[3].ContextWindow != 200000 || models[3].MaxOutputTokens != 10000 {
		t.Fatalf("openrouter = %+v", models[3])
	}
}

func TestProbeModelReturnsReply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
		})
	}))
	defer srv.Close()

	reply, err := ProbeModel(context.Background(), "openai", srv.URL, "k", "m")
	if err != nil {
		t.Fatalf("ProbeModel: %v", err)
	}
	if reply != "ok" {
		t.Fatalf("reply = %q, want ok", reply)
	}
}

func TestProbeProviderHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	if _, err := ProbeProvider(context.Background(), "openai", srv.URL, "k"); err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
}

func TestProbeUnsupportedKind(t *testing.T) {
	if _, err := ProbeProvider(context.Background(), "anthropic", "https://example.test/v1", "k"); err == nil {
		t.Fatal("expected unsupported kind error")
	}
	if _, err := ProbeModel(context.Background(), "google", "https://example.test/v1", "k", "m"); err == nil {
		t.Fatal("expected unsupported kind error")
	}
}

func TestProbeRejectsNonHTTPScheme(t *testing.T) {
	if _, err := ProbeProvider(context.Background(), "openai", "file:///etc/passwd", "k"); err == nil {
		t.Fatal("expected scheme validation error")
	}
}
