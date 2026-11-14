package adminclient

// The admin module authenticates its internal surface with the shared token. A
// helper that builds its own request — rather than going through Do — has to add
// that header too, or the call fails with a 401 that reads like a misconfigured
// deployment.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPutJSONPathCarriesTheToken(t *testing.T) {
	var gotAuth, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		gotAuth, gotPath, gotBody = r.Header.Get("Authorization"), r.URL.Path, string(raw)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"synced"}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "test-token")
	if _, err := client.PutJSONPath(context.Background(), "/api/v1/internal/builtin-tools/sync", []byte(`{"tools":[]}`)); err != nil {
		t.Fatalf("PutJSONPath: %v", err)
	}
	if gotAuth != "Bearer test-token" {
		t.Fatalf("the call went out without the token: %q", gotAuth)
	}
	if gotPath != "/api/v1/internal/builtin-tools/sync" || gotBody != `{"tools":[]}` {
		t.Fatalf("unexpected request: %s %s", gotPath, gotBody)
	}
}

func TestPutJSONPathReportsTheAdminStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "wrong")
	status, err := client.PutJSONPath(context.Background(), "/x", []byte(`{}`))
	if err == nil || status != http.StatusUnauthorized {
		t.Fatalf("expected the admin's 401 to surface, got %d %v", status, err)
	}
}
