package store

// The bus's collections are its own.
//
// Every other domain moved out: the catalogue is the admin module's, the agent
// rows and the conversations are the portal's, the queue and the runner registry
// are the provisioner's, the accounts are the auth service's. What is left here is
// bus_* (the two queues, the voice sessions, the WeChat KF cursor, the stream and
// platform event logs) and sys_* (the counters and the migration log).
//
// Two lines keep it that way: the names carry their writer's prefix, and a store
// file that spells another module's collection fails the build instead of quietly
// reading rows it does not own.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/stream"
	migrationsmongo "gitee.com/kobeyoung81/nanobus/migrations-mongo"
)

func TestCollectionNamesCarryTheirModulePrefix(t *testing.T) {
	owned := map[string][]string{
		"bus_": {
			inboundQueueCollection, outboundQueueCollection, wechatKFSyncStateCollection,
			VoiceSessionsCollection, stream.EventsCollection, stream.PlatformEventsCollection,
		},
		"sys_": {countersCollection, migrationsCollection},
	}
	for prefix, names := range owned {
		for _, name := range names {
			if !strings.HasPrefix(name, prefix) {
				t.Errorf("%q does not start with %q", name, prefix)
			}
		}
	}
}

// TestStoreNamesOnlyItsOwnCollections is the line that keeps a store file from
// reading another module's rows. It scans the package's own sources, so a new
// helper that reaches for admin_agent_types fails here rather than in production.
func TestStoreNamesOnlyItsOwnCollections(t *testing.T) {
	foreign := []string{`"admin_`, `"portal_`, `"provisioner_`, `"auth_`, `"workspace_`}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(raw)
		for _, prefix := range foreign {
			if strings.Contains(text, prefix) {
				t.Errorf("%s names another module's collection (%s…): the bus asks its owner instead of reading its rows", file, strings.TrimPrefix(prefix, `"`))
			}
		}
	}
}

// The manifests are what actually create the indexes, so a name that is only
// renamed in Go leaves production without its indexes — and nothing fails at
// startup. This walks the manifests instead of a second hand-written list.
func TestMigrationManifestsUsePrefixedCollections(t *testing.T) {
	manifests, err := migrationsmongo.Load()
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	for name, body := range manifests {
		var manifest struct {
			Collections map[string]json.RawMessage `json:"collections"`
		}
		if err := json.Unmarshal([]byte(body), &manifest); err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		for collection := range manifest.Collections {
			if !strings.HasPrefix(collection, "bus_") && !strings.HasPrefix(collection, "sys_") {
				t.Errorf("%s: collection %q is not one of the bus's", name, collection)
			}
		}
	}
}
