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
	"net/url"
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

// Model answers one row of the model catalogue.
func (c *Client) Model(ctx context.Context, id int64) (*nanoprotocol.ModelDef, error) {
	var out struct {
		Model *nanoprotocol.ModelDef `json:"model"`
	}
	if err := c.get(ctx, fmt.Sprintf("/api/v1/internal/models/%d", id), &out); err != nil {
		return nil, err
	}
	return out.Model, nil
}

// ModelProvider answers one provider row.
func (c *Client) ModelProvider(ctx context.Context, id int64) (*nanoprotocol.ModelProviderDef, error) {
	var out struct {
		Provider *nanoprotocol.ModelProviderDef `json:"provider"`
	}
	if err := c.get(ctx, fmt.Sprintf("/api/v1/internal/model-providers/%d", id), &out); err != nil {
		return nil, err
	}
	return out.Provider, nil
}

// PromptBlocks answers one agent's effective prompt blocks.
type PromptBlocks struct {
	Head       string            `json:"head"`
	HeadSource string            `json:"head_source"`
	ModeNotes  map[string]string `json:"mode_notes"`
}

// PromptBlocks answers what one agent runs with: the effective shared head and the
// two mode switch notes.
func (c *Client) PromptBlocks(ctx context.Context, agentID string) (*PromptBlocks, error) {
	var out PromptBlocks
	path := "/api/v1/internal/prompt-blocks?agent_id=" + url.QueryEscape(agentID)
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AgentTypes answers the type catalogue.
func (c *Client) AgentTypes(ctx context.Context, sessionOnly bool) ([]nanoprotocol.AgentTypeDef, error) {
	var out struct {
		Types []nanoprotocol.AgentTypeDef `json:"types"`
	}
	path := "/api/v1/internal/agent-types"
	if sessionOnly {
		path += "?session_only=1"
	}
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Types, nil
}

// AgentMode answers one (agent, mode): the baseline type and the user's override.
type AgentMode struct {
	Type     *nanoprotocol.AgentTypeDef      `json:"type"`
	Override *nanoprotocol.AgentTypeOverride `json:"override"`
}

// AgentMode answers what the per-agent mode editor reads.
func (c *Client) AgentMode(ctx context.Context, agentID, typeID string) (*AgentMode, error) {
	var out AgentMode
	path := "/api/v1/internal/agent-modes/" + url.PathEscape(agentID) + "/" + url.PathEscape(typeID)
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	if !c.Enabled() {
		return fmt.Errorf("the catalogue module is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call admin %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("call admin %s: status %d: %s", path, resp.StatusCode, bytes.TrimSpace(detail))
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
