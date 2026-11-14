// Package adminclient reads configuration from the admin module.
//
// The bus is not a configuration entry point any more: it serves messages, and
// the one thing it still needs from the admin domain — the voice settings its
// user-facing endpoint reports — it asks for over HTTP like every other module.
package adminclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// Client is a minimal HTTP client for one admin endpoint.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client. An empty base URL leaves the client disabled: Items
// then reports that clearly instead of calling ":8080".
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// Enabled reports whether a base URL was configured.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

// do sends the request and decodes a successful body into out.
func (c *Client) do(req *http.Request, out any) error {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call admin %s: %w", req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("call admin %s: status %d: %s", req.URL.Path, resp.StatusCode, bytes.TrimSpace(detail))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Do sends one request with the caller's body and returns the answer as-is, so a
// forwarding handler can pass the admin module's status and payload through.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader, contentType string) (int, []byte, error) {
	if !c.Enabled() {
		return 0, nil, fmt.Errorf("admin module is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, nil, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	req.Header.Set("Content-Type", contentType)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("call admin %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

// GetJSON reads one admin endpoint into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	if !c.Enabled() {
		return fmt.Errorf("admin module is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// PutJSONPath posts a body to an internal admin endpoint (the methods differ per
// endpoint; this one is a POST with a JSON body).
func (c *Client) PutJSONPath(ctx context.Context, path string, body []byte) (int, error) {
	if !c.Enabled() {
		return 0, fmt.Errorf("admin module is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	// The token is not optional here: the admin module refuses an anonymous call,
	// and a request without it fails with 401 that reads like a misconfiguration
	// rather than the missing header it is.
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("call admin %s: status %d: %s", path, resp.StatusCode, bytes.TrimSpace(detail))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// PutJSON sends a JSON body and reports the status, so a caller can pass the
// admin module's own verdict through instead of flattening it.
func (c *Client) PutJSON(ctx context.Context, path string, body []byte) (int, error) {
	if !c.Enabled() {
		return 0, fmt.Errorf("admin module is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("call admin %s: status %d: %s", path, resp.StatusCode, bytes.TrimSpace(detail))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// PermissionKeys returns the permission key catalog.
//
// The bus still validates a policy against the read-only keys when it saves an
// agent type; the catalog is the admin module's, so it asks.
func (c *Client) PermissionKeys(ctx context.Context) ([]nanoprotocol.PermissionKeyDef, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("admin module is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/permission-keys", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ask admin for permission keys: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("admin answered %d for permission keys", resp.StatusCode)
	}
	var payload nanoprotocol.PermissionKeysResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode permission keys: %w", err)
	}
	return payload.Keys, nil
}

// Items returns one component's configuration.
//
// A non-200 is an error, never an empty list: "the admin module said no" and
// "this component has no settings" must not look the same to the caller, or a
// broken deployment silently runs on defaults.
func (c *Client) Items(ctx context.Context, componentType string) ([]store.ConfigItem, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("admin module is not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/api/v1/config/"+url.PathEscape(componentType), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ask admin for %s config: %w", componentType, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("admin answered %d for %s config", resp.StatusCode, componentType)
	}
	var payload struct {
		ComponentType string             `json:"component_type"`
		Items         []store.ConfigItem `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode %s config: %w", componentType, err)
	}
	return payload.Items, nil
}
