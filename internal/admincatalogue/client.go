package admincatalogue

// The catalogue, as an API.
//
// The bus used to read admin_agent_types, admin_models, admin_prompts,
// admin_skills, admin_mcp_servers, admin_builtin_tools and
// admin_agent_type_overrides straight out of the shared database and merge them
// into the snapshot an agent starts with. Those collections have one writer and it
// is the admin module, so the merge moved there and this asks for the answer.
//
// The agent-side facts the resolution needs — which types one agent runs, which is
// its default — are the agent row's, and the rows are the portal's. The caller
// reads the row (it already does) and passes those facts in.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// Client asks the admin module to resolve one agent's runtime.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds the client. An empty base URL disables it: the caller then keeps the
// bus's own resolution path, which is the rollback switch.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// Enabled reports whether a base URL was configured.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

// RuntimeInput is the agent-side half of the resolution.
type RuntimeInput struct {
	AgentID      string   `json:"agent_id"`
	UserID       string   `json:"user_id"`
	PrimaryType  string   `json:"primary_type,omitempty"`
	SessionTypes []string `json:"session_types,omitempty"`
	DefaultType  string   `json:"default_type,omitempty"`
}

// RuntimeResolution is what the admin module answers.
type RuntimeResolution struct {
	DefaultModeTypeID string                             `json:"default_mode_type_id"`
	Capabilities      *nanoprotocol.CapabilitiesResponse `json:"capabilities"`
	Settings          map[string]string                  `json:"settings"`
	Definitions       []nanoprotocol.AgentTypeDef        `json:"definitions"`
	Type              *nanoprotocol.AgentTypeDef         `json:"type"`
}

// ResolveRuntime answers the runtime view of one agent.
func (c *Client) ResolveRuntime(ctx context.Context, in RuntimeInput) (*RuntimeResolution, error) {
	var out RuntimeResolution
	if err := c.post(ctx, "/api/v1/internal/agent-runtime", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ModeCapabilities answers one (agent, mode) catalogue snapshot.
func (c *Client) ModeCapabilities(ctx context.Context, agentID, userID, typeID string) (*nanoprotocol.CapabilitiesResponse, error) {
	var out nanoprotocol.CapabilitiesResponse
	body := map[string]string{"agent_id": agentID, "user_id": userID, "type_id": typeID}
	if err := c.post(ctx, "/api/v1/internal/mode-capabilities", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	if !c.Enabled() {
		return fmt.Errorf("the catalogue module is not configured")
	}
	payload, err := json.Marshal(in)
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
		return fmt.Errorf("call admin %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("call admin %s: status %d: %s", path, resp.StatusCode, bytes.TrimSpace(detail))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
