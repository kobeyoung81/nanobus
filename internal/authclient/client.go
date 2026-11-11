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

// UserSecrets returns the stored secret values for one capability. They are for
// the platform's own use: the console masks them before a person sees them.
func (c *Client) UserSecrets(ctx context.Context, userID, kind, ref string) (map[string]string, error) {
	path := "/internal/v1/users/" + url.PathEscape(userID) + "/secrets/" + url.PathEscape(kind) + "/" + url.PathEscape(ref)
	var payload struct {
		Secrets map[string]string `json:"secrets"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return nil, err
	}
	if payload.Secrets == nil {
		payload.Secrets = map[string]string{}
	}
	return payload.Secrets, nil
}

// SetUserSecret stores (or replaces) one secret value.
func (c *Client) SetUserSecret(ctx context.Context, userID, kind, ref, key, value string) error {
	body, err := json.Marshal(map[string]string{"key": key, "value": value})
	if err != nil {
		return err
	}
	path := "/internal/v1/users/" + url.PathEscape(userID) + "/secrets/" + url.PathEscape(kind) + "/" + url.PathEscape(ref)
	return c.do(ctx, http.MethodPut, path, bytes.NewReader(body), nil)
}

// DeleteUserSecret removes one secret value.
func (c *Client) DeleteUserSecret(ctx context.Context, userID, kind, ref, key string) error {
	path := "/internal/v1/users/" + url.PathEscape(userID) + "/secrets/" + url.PathEscape(kind) + "/" + url.PathEscape(ref) +
		"?key=" + url.QueryEscape(key)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// OAuthLink is one identity link: this account signs in as that provider identity.
type OAuthLink struct {
	UserID     string `json:"user_id"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
}

// OAuthLinks lists the identities linked to one account.
func (c *Client) OAuthLinks(ctx context.Context, userID string) ([]OAuthLink, error) {
	var payload struct {
		Links []OAuthLink `json:"links"`
	}
	if err := c.do(ctx, http.MethodGet, "/internal/v1/users/"+url.PathEscape(userID)+"/oauth-links", nil, &payload); err != nil {
		return nil, err
	}
	if payload.Links == nil {
		payload.Links = []OAuthLink{}
	}
	return payload.Links, nil
}

// OAuthLinksByProvider is the roster one provider signs in with.
func (c *Client) OAuthLinksByProvider(ctx context.Context, provider string) ([]OAuthLink, error) {
	var payload struct {
		Links []OAuthLink `json:"links"`
	}
	path := "/internal/v1/oauth-links?provider=" + url.QueryEscape(provider)
	if err := c.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return nil, err
	}
	if payload.Links == nil {
		payload.Links = []OAuthLink{}
	}
	return payload.Links, nil
}

// LinkOAuth links (or moves) one provider identity.
func (c *Client) LinkOAuth(ctx context.Context, userID, provider, providerID string) error {
	body, err := json.Marshal(map[string]string{"provider_id": providerID})
	if err != nil {
		return err
	}
	path := "/internal/v1/users/" + url.PathEscape(userID) + "/oauth-links/" + url.PathEscape(provider)
	return c.do(ctx, http.MethodPost, path, bytes.NewReader(body), nil)
}

// UnlinkOAuth removes an account's link for one provider.
func (c *Client) UnlinkOAuth(ctx context.Context, userID, provider string) error {
	path := "/internal/v1/users/" + url.PathEscape(userID) + "/oauth-links/" + url.PathEscape(provider)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// ======== accounts ========

// ResolvedAccount is onboarding's answer.
type ResolvedAccount struct {
	UserID    string `json:"user_id"`
	BindingID int64  `json:"binding_id"`
	IsNew     bool   `json:"is_new"`
}

// ErrChannelDisabled reports a channel an administrator switched off; the inbound
// path answers the sender with that.
var ErrChannelDisabled = errors.New("channel disabled")

// Account is one account as the console reads it.
type Account struct {
	UserID             string `json:"user_id"`
	DisplayName        string `json:"display_name"`
	Email              string `json:"email"`
	Role               string `json:"role"`
	Status             string `json:"status"`
	Tier               string `json:"tier"`
	MustChangePassword bool   `json:"must_change_password"`
	PreferredLanguage  string `json:"preferred_language"`
	UpdatedAt          string `json:"updated_at"`
}

// AccountPatch names the fields a request may change; nil means "not mentioned".
type AccountPatch struct {
	Status            *string `json:"status"`
	Role              *string `json:"role"`
	Tier              *string `json:"tier"`
	DisplayName       *string `json:"display_name"`
	Email             *string `json:"email"`
	PreferredLanguage *string `json:"preferred_language"`
}

// ResolveAccount is onboarding: whose account is this identity, creating one if
// nobody's.
func (c *Client) ResolveAccount(ctx context.Context, channel, senderID string) (ResolvedAccount, error) {
	body, err := json.Marshal(map[string]string{"channel": channel, "sender_id": senderID})
	if err != nil {
		return ResolvedAccount{}, err
	}
	var out ResolvedAccount
	err = c.do(ctx, http.MethodPost, "/internal/v1/accounts/resolve", bytes.NewReader(body), &out)
	if status, ok := err.(*StatusError); ok && status.Status == http.StatusConflict {
		return ResolvedAccount{}, ErrChannelDisabled
	}
	return out, err
}

// RegisterAccount creates an account (or refreshes its display name).
func (c *Client) RegisterAccount(ctx context.Context, userID, displayName string) error {
	body, err := json.Marshal(map[string]string{"user_id": userID, "display_name": displayName})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/internal/v1/accounts", bytes.NewReader(body), nil)
}

// LinkChannel links one platform identity to an account.
func (c *Client) LinkChannel(ctx context.Context, userID, channel, senderID string) error {
	body, err := json.Marshal(map[string]string{"channel": channel, "sender_id": senderID})
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPost, "/internal/v1/accounts/"+url.PathEscape(userID)+"/channels", bytes.NewReader(body), nil)
}

// Account reads one account by id.
func (c *Client) Account(ctx context.Context, userID string) (*Account, error) {
	var out Account
	if err := c.do(ctx, http.MethodGet, "/internal/v1/accounts/"+url.PathEscape(userID), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AccountByEmail reads one account by address.
func (c *Client) AccountByEmail(ctx context.Context, email string) (*Account, error) {
	var out Account
	path := "/internal/v1/accounts?email=" + url.QueryEscape(email)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) listAccounts(ctx context.Context, query string) ([]Account, error) {
	var payload struct {
		Accounts []Account `json:"accounts"`
	}
	if err := c.do(ctx, http.MethodGet, "/internal/v1/accounts?"+query, nil, &payload); err != nil {
		return nil, err
	}
	if payload.Accounts == nil {
		payload.Accounts = []Account{}
	}
	return payload.Accounts, nil
}

// ListAccounts is the console's page.
func (c *Client) ListAccounts(ctx context.Context, includeDeleted bool, limit int) ([]Account, error) {
	query := "limit=" + url.QueryEscape(fmt.Sprint(limit))
	if includeDeleted {
		query += "&include_deleted=1"
	}
	return c.listAccounts(ctx, query)
}

// SearchAccounts is the console's search box.
func (c *Client) SearchAccounts(ctx context.Context, q string, limit int) ([]Account, error) {
	return c.listAccounts(ctx, "q="+url.QueryEscape(q)+"&limit="+url.QueryEscape(fmt.Sprint(limit)))
}

// AllUserIDs is the set of every account id.
func (c *Client) AllUserIDs(ctx context.Context) ([]string, error) {
	var payload struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := c.do(ctx, http.MethodGet, "/internal/v1/accounts?ids_only=1", nil, &payload); err != nil {
		return nil, err
	}
	if payload.UserIDs == nil {
		payload.UserIDs = []string{}
	}
	return payload.UserIDs, nil
}

// PatchAccount changes the fields the patch mentions.
func (c *Client) PatchAccount(ctx context.Context, userID string, patch AccountPatch) error {
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	return c.do(ctx, http.MethodPatch, "/internal/v1/accounts/"+url.PathEscape(userID), bytes.NewReader(body), nil)
}

// PasswordHash reads the stored credential.
func (c *Client) PasswordHash(ctx context.Context, userID string) (string, error) {
	var payload struct {
		Hash string `json:"password_hash"`
	}
	path := "/internal/v1/accounts/" + url.PathEscape(userID) + "/password"
	if err := c.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return "", err
	}
	return payload.Hash, nil
}

// SetPassword writes the credential. keepFlag keeps the "must change" flag as it
// is, which is what a password the person chose themselves does.
func (c *Client) SetPassword(ctx context.Context, userID, hash string, mustChange, keepFlag bool) error {
	body, err := json.Marshal(map[string]any{
		"password_hash": hash, "must_change_password": mustChange, "keep_flag": keepFlag,
	})
	if err != nil {
		return err
	}
	path := "/internal/v1/accounts/" + url.PathEscape(userID) + "/password"
	return c.do(ctx, http.MethodPut, path, bytes.NewReader(body), nil)
}

// ClaimBootstrapAdmin takes the one-shot administrator hand-out.
func (c *Client) ClaimBootstrapAdmin(ctx context.Context, userID string) (bool, error) {
	var payload struct {
		Claimed bool `json:"claimed"`
	}
	path := "/internal/v1/accounts/" + url.PathEscape(userID) + "/bootstrap-admin"
	if err := c.do(ctx, http.MethodPost, path, bytes.NewReader([]byte("{}")), &payload); err != nil {
		return false, err
	}
	return payload.Claimed, nil
}

// ReleaseBootstrapAdmin gives the hand-out back after a failed claim.
func (c *Client) ReleaseBootstrapAdmin(ctx context.Context, userID string) error {
	path := "/internal/v1/accounts/" + url.PathEscape(userID) + "/bootstrap-admin"
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// Channels lists an account's channel names.
func (c *Client) Channels(ctx context.Context, userID string) ([]string, error) {
	var payload struct {
		Channels []string `json:"channels"`
	}
	path := "/internal/v1/accounts/" + url.PathEscape(userID) + "/channels"
	if err := c.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return nil, err
	}
	if payload.Channels == nil {
		payload.Channels = []string{}
	}
	return payload.Channels, nil
}

// SetChannel enables or disables one channel of one account.
func (c *Client) SetChannel(ctx context.Context, userID, channel string, enabled bool) error {
	body, err := json.Marshal(map[string]bool{"enabled": enabled})
	if err != nil {
		return err
	}
	path := "/internal/v1/accounts/" + url.PathEscape(userID) + "/channels/" + url.PathEscape(channel)
	return c.do(ctx, http.MethodPatch, path, bytes.NewReader(body), nil)
}

// AccountInfo is the runtime's question about one account.
func (c *Client) AccountInfo(ctx context.Context, userID string) (string, string, error) {
	var payload struct {
		Status string `json:"status"`
		Tier   string `json:"tier"`
	}
	path := "/internal/v1/accounts/" + url.PathEscape(userID) + "/info"
	if err := c.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return "", "", err
	}
	return payload.Status, payload.Tier, nil
}

// AccountCounts is the console's overview.
func (c *Client) AccountCounts(ctx context.Context) (int, int, map[string]int, error) {
	var payload struct {
		Active int            `json:"active"`
		Admins int            `json:"admins"`
		Tiers  map[string]int `json:"tiers"`
	}
	if err := c.do(ctx, http.MethodGet, "/internal/v1/accounts/counts", nil, &payload); err != nil {
		return 0, 0, nil, err
	}
	if payload.Tiers == nil {
		payload.Tiers = map[string]int{}
	}
	return payload.Active, payload.Admins, payload.Tiers, nil
}

// UserExists reports whether an account exists.
func (c *Client) UserExists(ctx context.Context, userID string) (bool, error) {
	var payload struct {
		Exists bool `json:"exists"`
	}
	path := "/internal/v1/accounts/" + url.PathEscape(userID) + "/exists"
	if err := c.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return false, err
	}
	return payload.Exists, nil
}

// BindingOwner answers whose binding id this is.
func (c *Client) BindingOwner(ctx context.Context, bindingID int64) (string, error) {
	var payload struct {
		UserID string `json:"user_id"`
	}
	path := "/internal/v1/bindings/" + url.PathEscape(fmt.Sprint(bindingID)) + "/account"
	if err := c.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		return "", err
	}
	return payload.UserID, nil
}
