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

// EnsureDefaultAgent makes sure an account has an agent and returns it. Onboarding
// uses it: an account created by a channel identity arrives without one, and a
// container with no agent answers nothing.
func (c *Client) EnsureDefaultAgent(ctx context.Context, userID string) (string, error) {
	var out struct {
		AgentID string `json:"agent_id"`
	}
	path := "/internal/v1/users/" + url.PathEscape(userID) + "/agents/ensure"
	if err := c.post(ctx, path, map[string]any{}, &out); err != nil {
		return "", err
	}
	if out.AgentID == "" {
		return "", fmt.Errorf("the portal answered an empty agent id for %s", userID)
	}
	return out.AgentID, nil
}

// GetJSON reads one internal endpoint.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	req, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	_, err = c.do(req, out)
	return err
}

// PostJSON sends an already-encoded body and reports the status, so a caller can
// recognise a 409 without decoding it as a failure.
func (c *Client) PostJSON(ctx context.Context, path string, body []byte, out any) (int, error) {
	req, err := c.request(ctx, http.MethodPost, path, body)
	if err != nil {
		return 0, err
	}
	return c.do(req, out)
}

func (c *Client) post(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := c.request(ctx, http.MethodPost, path, payload)
	if err != nil {
		return err
	}
	_, err = c.do(req, out)
	return err
}

func (c *Client) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("portal module is not configured")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	return req, nil
}

// do performs the request and reports the status code. A 4xx/5xx is returned as
// both an error and a status, so a caller that treats one code specially (409 →
// resend everything) does not have to parse the message.
func (c *Client) do(req *http.Request, out any) (int, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("call portal %s: %w", req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("call portal %s: status %d: %s", req.URL.Path, resp.StatusCode, bytes.TrimSpace(detail))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}
