// Package llmprobe performs a lightweight connectivity check against an
// OpenAI-compatible provider. It backs the bus-side "test connection" action
// for model catalog entries. Only the OpenAI-compatible surface is implemented
// because that is the only provider kind the runtime supports.
package llmprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ModelInfo is one model exposed by a provider's /models endpoint. Context
// window and max output tokens are best-effort: providers that do not report
// them leave the fields at zero.
type ModelInfo struct {
	ID              string `json:"id"`
	ContextWindow   int    `json:"context_window,omitempty"`
	MaxOutputTokens int    `json:"max_output_tokens,omitempty"`
}

// ProbeProvider lists the models the provider exposes (validates base URL +
// API key). Returns the model ids plus any metadata the provider reports.
func ProbeProvider(ctx context.Context, kind, baseURL, apiKey string) ([]ModelInfo, error) {
	if err := validate(kind, baseURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint(baseURL, "models"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	setAuth(req, apiKey)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, trim(body))
	}
	var payload struct {
		Data []struct {
			ID               string `json:"id"`
			ContextLength    int    `json:"context_length"`
			MaxOutputTokens  int    `json:"max_output_tokens"`
			InputTokenLimit  int    `json:"input_token_limit"`
			OutputTokenLimit int    `json:"output_token_limit"`
			TopProvider      struct {
				ContextLength       int `json:"context_length"`
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("unexpected response: %w", err)
	}
	out := make([]ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, ModelInfo{
			ID: m.ID,
			ContextWindow: firstNonZero(
				m.ContextLength, m.InputTokenLimit, m.TopProvider.ContextLength,
			),
			MaxOutputTokens: firstNonZero(
				m.MaxOutputTokens, m.OutputTokenLimit, m.TopProvider.MaxCompletionTokens,
			),
		})
	}
	return out, nil
}

// firstNonZero returns the first positive value, or 0 when none are set.
func firstNonZero(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

// ProbeModel sends a single-token completion to validate that the model works
// with the given credentials, and returns the reply text.
func ProbeModel(ctx context.Context, kind, baseURL, apiKey, model string) (string, error) {
	if err := validate(kind, baseURL); err != nil {
		return "", err
	}
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("model is required")
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]any{{"role": "user", "content": "Reply with the single word: ok"}},
		"max_tokens": 1,
		"stream":     false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint(baseURL, "chat/completions"), bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	setAuth(req, apiKey)

	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return "", fmt.Errorf("provider unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, trim(body))
	}
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("unexpected response: %w", err)
	}
	if len(payload.Choices) == 0 {
		return "", fmt.Errorf("empty completion")
	}
	return payload.Choices[0].Message.Content, nil
}

func validate(kind, baseURL string) error {
	switch kind {
	case "", "openai":
	default:
		return fmt.Errorf("provider kind %q is not supported for testing", kind)
	}
	if baseURL == "" {
		return fmt.Errorf("base URL is required")
	}
	if u, err := url.Parse(baseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("base URL must be an http(s) URL")
	}
	return nil
}

// Endpoint joins a provider base URL (which already carries its API prefix,
// e.g. https://host/v1) with a path. It mirrors how every OpenAI-compatible
// call in the bus addresses a provider.
func Endpoint(baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + "/" + path
}

func setAuth(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func trim(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
