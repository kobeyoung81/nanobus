// Package provisionerclient talks to the provisioning module.
//
// The provisioning queue belongs to the runner: it polls it and records what
// happened. The bus still asks the owner for the two things that are its own —
// an agent's container request, and the runner registry — while the console's
// container lifecycle actions moved to the admin module (which has its own
// client for them).
package provisionerclient

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

	"gitee.com/kobeyoung81/nanoprotocol"
)

// Client calls the provisioner's internal API.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	// tunnelHTTP is slower than http: a forwarded request waits for the
	// container's bridge, which itself allows 60s for a workspace file.
	tunnelHTTP *http.Client
}

// New returns a client. An empty base URL leaves it disabled and every call then
// fails loudly.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		http:       &http.Client{Timeout: 15 * time.Second},
		tunnelHTTP: &http.Client{Timeout: 90 * time.Second},
	}
}

// Enabled reports whether a base URL was configured.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

// EnqueueForAgent asks for a container for one agent, using the owner's tier.
func (c *Client) EnqueueForAgent(ctx context.Context, userID, agentID string) error {
	body := map[string]string{"user_id": userID, "agent_id": agentID}
	return c.post(ctx, "/api/v1/internal/container-requests", body)
}

// Enqueue creates a request for one (user, binding) pair.
func (c *Client) Enqueue(ctx context.Context, userID string, bindingID int64, agentID, level string) error {
	body := map[string]any{"user_id": userID, "binding_id": bindingID, "agent_id": agentID, "level": level}
	return c.post(ctx, "/api/v1/internal/containers", body)
}

// RegisterRunner asks the registry's owner to record (or refresh) a runner. The
// runner registers itself too; this path stays for older runners that still call
// the bus's endpoint.
func (c *Client) RegisterRunner(ctx context.Context, runnerID, hostname, dockerHost string) error {
	body := map[string]string{"runner_id": runnerID, "hostname": hostname, "docker_host": dockerHost}
	return c.do(ctx, http.MethodPost, "/api/v1/internal/runners", body)
}

// DeleteRunner prunes a dead runner row.
func (c *Client) DeleteRunner(ctx context.Context, runnerID string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/internal/runners/"+url.PathEscape(runnerID), nil)
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	return c.do(ctx, http.MethodPost, path, body)
}

func (c *Client) do(ctx context.Context, method, path string, body any) error {
	if !c.Enabled() {
		return fmt.Errorf("the provisioner module is not configured")
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call the provisioner %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(detail))}
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// ForwardTunnel puts one HTTP request on the container's tunnel. Tunnels
// terminate at the provisioner now, so a request for a container that connected
// there has to be relayed; a 404 means no tunnel with that name is registered.
func (c *Client) ForwardTunnel(ctx context.Context, name string, req nanoprotocol.TunnelRequest) (*nanoprotocol.TunnelResponse, error) {
	if !c.Enabled() {
		return nil, fmt.Errorf("the provisioner module is not configured")
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	endpoint := c.baseURL + "/api/v1/internal/tunnels/" + url.PathEscape(name) + "/request"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.tunnelHTTP.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("forward tunnel request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(detail))}
	}
	var out nanoprotocol.TunnelResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode tunnel response: %w", err)
	}
	return &out, nil
}

// StatusError carries the answer's status so the caller can pass 404/409 through
// instead of flattening them into 502.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("provisioner answered %d: %s", e.Code, e.Body)
}

// CodeOf reports the status a call answered with, or 0 when it never got one.
func CodeOf(err error) int {
	if se, ok := err.(*StatusError); ok {
		return se.Code
	}
	return 0
}
