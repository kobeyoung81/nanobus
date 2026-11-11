// Package authclient talks to nanoauth's internal account surface.
//
// Accounts, channel links, OAuth links, refresh tickets and a user's starred git
// paths are identity's, so nanoauth owns the collections. The bus still serves the
// endpoints its console and runtime call; what it serves, it now forwards.
package authclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound reports a 404 from nanoauth, so a handler can keep its own answer
// ("no such shortcut") apart from a failure.
var ErrNotFound = errors.New("not found")

// StatusError is a non-2xx answer that is not a 404. The handler passes the
// status and the module's message through, so the console hears why.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("nanoauth answered %d: %s", e.Status, e.Body)
}

// Client is a minimal HTTP client for nanoauth's /internal/v1 surface.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client. An empty base URL leaves it disabled: every call then
// reports that clearly instead of dialling ":0".
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether a base URL was configured.
func (c *Client) Enabled() bool { return c != nil && c.baseURL != "" }

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	if !c.Enabled() {
		return fmt.Errorf("the auth module is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call auth %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		_, _ = io.Copy(io.Discard, resp.Body)
		return ErrNotFound
	}
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Status: resp.StatusCode, Body: strings.TrimSpace(string(detail))}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GitShortcut is one starred path inside an agent's container.
type GitShortcut struct {
	ID      int64  `json:"id"`
	UserID  string `json:"user_id"`
	AgentID string `json:"agent_id"`
	Path    string `json:"path"`
	Label   string `json:"label"`
}

// GitShortcuts lists a user's starred paths, optionally narrowed to one agent.
func (c *Client) GitShortcuts(ctx context.Context, userID, agentID string) ([]GitShortcut, error) {
	path := "/internal/v1/users/" + url.PathEscape(userID) + "/git-shortcuts"
	if agentID != "" {
		path += "?agent_id=" + url.QueryEscape(agentID)
	}
	var payload struct {
		Shortcuts []GitShortcut `json:"shortcuts"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return nil, err
	}
	if payload.Shortcuts == nil {
		payload.Shortcuts = []GitShortcut{}
	}
	return payload.Shortcuts, nil
}

// SaveGitShortcut stars a path, or re-labels it when it is already starred.
func (c *Client) SaveGitShortcut(ctx context.Context, userID, agentID, path, label string) (GitShortcut, error) {
	body, err := json.Marshal(map[string]string{"agent_id": agentID, "path": path, "label": label})
	if err != nil {
		return GitShortcut{}, err
	}
	var out GitShortcut
	err = c.do(ctx, http.MethodPost, "/internal/v1/users/"+url.PathEscape(userID)+"/git-shortcuts", bytes.NewReader(body), &out)
	return out, err
}

// DeleteGitShortcut removes one star.
func (c *Client) DeleteGitShortcut(ctx context.Context, userID string, id int64) error {
	path := "/internal/v1/users/" + url.PathEscape(userID) + "/git-shortcuts/" + url.PathEscape(fmt.Sprint(id))
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}
