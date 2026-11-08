package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
)

// httpClient is shared by the one-shot calls; Context bounds a single request,
// the client timeout is the backstop.
var httpClient = &http.Client{Timeout: CallTimeout}

// TranscribeFile sends one recorded file to the provider's transcription
// endpoint and returns the transcript. It backs the batch pass and the admin
// "test recognition" button; the browser's live dictation uses the realtime
// session instead.
//
// language and prompt are optional hints: journals of OpenAI-compatible providers
// differ in whether they accept them, so an empty value is left out entirely
// rather than sent as an empty string.
func TranscribeFile(ctx context.Context, p Provider, model, language, prompt, filename string, audio []byte) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("build multipart: %w", err)
	}
	if _, err := part.Write(audio); err != nil {
		return "", fmt.Errorf("build multipart: %w", err)
	}
	if err := writer.WriteField("model", model); err != nil {
		return "", fmt.Errorf("build multipart: %w", err)
	}
	if hint := strings.TrimSpace(language); hint != "" {
		if err := writer.WriteField("language", hint); err != nil {
			return "", fmt.Errorf("build multipart: %w", err)
		}
	}
	if hint := strings.TrimSpace(prompt); hint != "" {
		if err := writer.WriteField("prompt", hint); err != nil {
			return "", fmt.Errorf("build multipart: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("build multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TranscriptionURL(p.BaseURL), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	setAuth(req, p.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("transcription request failed: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("transcription HTTP %d: %s", resp.StatusCode, trimBody(payload))
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", fmt.Errorf("unexpected transcription response: %w", err)
	}
	return strings.TrimSpace(parsed.Text), nil
}

// Polish rewrites a transcript into a sendable message with a chat model. An
// empty model (or an empty response) is reported as an error so the caller can
// keep the raw transcript.
//
// reasoningEffort caps the model's thinking, with "none" meaning "do not
// reason". An empty value keeps the parameter out of the request entirely,
// which is the escape hatch for a model that rejects the field; the config
// layer never produces one (an unset polish.thinking reads as "none").
func Polish(ctx context.Context, p Provider, model, prompt, text, reasoningEffort string) (string, error) {
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("no polish model configured")
	}
	requestBody := map[string]any{
		"model":       model,
		"temperature": 0,
		"stream":      false,
		"messages": []map[string]string{
			{"role": "system", "content": prompt},
			{"role": "user", "content": text},
		},
	}
	if reasoningEffort != "" {
		requestBody["reasoning_effort"] = reasoningEffort
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ChatURL(p.BaseURL), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req, p.APIKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("polish request failed: %w", err)
	}
	defer resp.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("polish HTTP %d: %s", resp.StatusCode, trimBody(payload))
	}

	// Providers differ in how they shape the content: a plain string, or a list
	// of content parts. A reasoning-only reply has no text at all and is an
	// error here (the caller then keeps the raw transcript).
	var parsed struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", fmt.Errorf("unexpected polish response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("polish response has no choices")
	}
	content := strings.TrimSpace(decodeContent(parsed.Choices[0].Message.Content))
	if content == "" {
		return "", fmt.Errorf("polish response has no text")
	}
	return content, nil
}

// decodeContent accepts both content shapes an OpenAI-compatible provider may
// return: a JSON string, or a list of parts with a "text" field.
func decodeContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return ""
}

func setAuth(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func trimBody(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
