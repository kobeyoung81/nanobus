package portalagents

// The agent rows, over the portal's internal API.
//
// The bus used to read portal_agents, portal_agent_groups and
// portal_binding_preferences straight out of the shared database: it needed the
// row to answer "who owns this agent" and to assemble a start snapshot. The rows
// are the portal's, so it answers instead.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Agent is one row as the bus needs it. The fields the portal exports under
// different names (SessionAgents is a list of ids there) are the ones a snapshot
// needs, not a mirror of the document.
type Agent struct {
	ID            string            `json:"id"`
	UserID        string            `json:"user_id"`
	TypeID        string            `json:"type_id"`
	GroupID       string            `json:"group_id"`
	DisplayName   string            `json:"display_name"`
	Description   string            `json:"description"`
	SessionAgents []string          `json:"session_agents"`
	DefaultAgent  string            `json:"default_agent"`
	Settings      map[string]string `json:"settings"`
	MaxIterations int               `json:"max_iterations"`
	Status        string            `json:"status"`
}

// Client reads agent rows from the portal.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds the client. An empty base URL disables it: the caller keeps its own
// read, which is the switch a rolling upgrade needs.
func New(baseURL, token string) *Client {
	return &Client{baseURL: baseURL, token: token, http: &http.Client{Timeout: 15 * time.Second}}
}

// Enabled reports whether a base URL was configured.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

// GetAgent returns one row, or nil when there is none.
func (c *Client) GetAgent(ctx context.Context, agentID string) (*Agent, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("the portal is not configured")
	}
	path := "/internal/v1/agents/" + url.PathEscape(agentID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call portal %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("call portal %s: status %d: %s", path, resp.StatusCode, bytes.TrimSpace(detail))
	}
	var payload struct {
		Agent Agent `json:"agent"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode agent: %w", err)
	}
	if payload.Agent.ID == "" {
		return nil, nil
	}
	return &payload.Agent, nil
}
