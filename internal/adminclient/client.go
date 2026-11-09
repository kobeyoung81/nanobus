// Package adminclient reads configuration from the admin module.
//
// The bus is not a configuration entry point any more: it serves messages, and
// the one thing it still needs from the admin domain — the voice settings its
// user-facing endpoint reports — it asks for over HTTP like every other module.
package adminclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
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
