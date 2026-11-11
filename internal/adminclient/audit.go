package adminclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// AuditEntry is what a caller knows about an action: who did it, what it was,
// and what it touched. The id and the timestamp belong to the admin module,
// which owns the trail.
type AuditEntry struct {
	ActorID      string         `json:"actor_id"`
	ActorRole    string         `json:"actor_role"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Detail       map[string]any `json:"detail,omitempty"`
	IP           string         `json:"ip"`
}

// RecordAudit appends one entry to the audit trail.
//
// The bus is not the trail's writer any more: it forwards what its own handlers
// did, the same way the portal does, and the admin module assigns the id and the
// time. Callers treat a failure as a warning — an operation log must not fail
// the operation it describes.
func (c *Client) RecordAudit(ctx context.Context, entry AuditEntry) error {
	if !c.Enabled() {
		return fmt.Errorf("admin module is not configured")
	}
	body, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode audit entry: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/internal/audit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("record audit: status %d: %s", resp.StatusCode, bytes.TrimSpace(detail))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}
