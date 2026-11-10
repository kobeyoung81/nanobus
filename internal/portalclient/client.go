// Package portalclient calls the portal's internal endpoints.
//
// The agent rows and their groups belong to the portal service now. The bus
// still faces the agents' supervisors and the gateway, which talk to it in the
// protocol they have always used, so the two operations that are not plain reads
// — reporting a runtime status and clearing a deleted type's references — are
// forwarded here instead of being written by the bus.
package portalclient

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
)

// Client is a minimal HTTP client for the portal's internal API.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client. An empty base URL leaves it disabled, and every call
// then fails loudly rather than calling "http://:80".
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether a base URL was configured.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

// SetAgentStatus records a runtime status report for an agent.
func (c *Client) SetAgentStatus(ctx context.Context, agentID, status string) error {
	body := map[string]string{"status": status}
	path := "/internal/v1/agents/" + url.PathEscape(agentID) + "/status"
	return c.post(ctx, path, body, nil)
}

// ClearTypeRefs unsets the type on every agent pointing at it, and returns how
// many rows changed.
func (c *Client) ClearTypeRefs(ctx context.Context, typeID string) (int, error) {
	var out struct {
		Cleared int `json:"cleared"`
	}
	path := "/internal/v1/agent-types/" + url.PathEscape(typeID) + "/clear-refs"
	if err := c.post(ctx, path, map[string]any{}, &out); err != nil {
		return 0, err
	}
	return out.Cleared, nil
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	if !c.Enabled() {
		return fmt.Errorf("portal module is not configured")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call portal %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("call portal %s: status %d: %s", path, resp.StatusCode, bytes.TrimSpace(detail))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
