package portalclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetAgentStatusPostsTheStatusWithTheToken(t *testing.T) {
	var gotPath, gotToken, gotStatus string
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get("Authorization")
		var body struct {
			Status string `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotStatus = body.Status
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"agent-1","status":"running"}`))
	}))
	defer portal.Close()

	client := New(portal.URL, "s3cret")
	if err := client.SetAgentStatus(context.Background(), "agent-1", "running"); err != nil {
		t.Fatalf("SetAgentStatus: %v", err)
	}
	if gotPath != "/internal/v1/agents/agent-1/status" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotStatus != "running" {
		t.Fatalf("status = %q", gotStatus)
	}
	if gotToken != "Bearer s3cret" {
		t.Fatalf("token = %q", gotToken)
	}
}

func TestClearTypeRefsReturnsTheCount(t *testing.T) {
	var gotPath string
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type_id":"tmp","cleared":3}`))
	}))
	defer portal.Close()

	cleared, err := New(portal.URL, "").ClearTypeRefs(context.Background(), "tmp")
	if err != nil {
		t.Fatalf("ClearTypeRefs: %v", err)
	}
	if gotPath != "/internal/v1/agent-types/tmp/clear-refs" {
		t.Fatalf("path = %q", gotPath)
	}
	if cleared != 3 {
		t.Fatalf("cleared = %d, want 3", cleared)
	}
}

// A disabled client must fail loudly: a silently dropped status report leaves a
// crashed agent looking alive.
func TestDisabledClientFails(t *testing.T) {
	if New("", "t").Enabled() {
		t.Fatal("an empty base URL must leave the client disabled")
	}
	if err := New("", "t").SetAgentStatus(context.Background(), "agent-1", "running"); err == nil {
		t.Fatal("expected an error from a disabled client")
	}
}

func TestErrorStatusIsReported(t *testing.T) {
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"agent not found"}`, http.StatusNotFound)
	}))
	defer portal.Close()

	if err := New(portal.URL, "").SetAgentStatus(context.Background(), "missing", "running"); err == nil {
		t.Fatal("expected the portal's 404 to surface as an error")
	}
}
