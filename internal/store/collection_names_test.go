package store

import (
	"encoding/json"
	"strings"
	"testing"

	migrationsmongo "gitee.com/kobeyoung81/nanobus/migrations-mongo"
)

// The prefix is the writer boundary: one collection, one writer, and the name
// says which module that is. A collection that loses its prefix looks like it
// belongs to nobody, and the next module to need it would write it too.
func TestCollectionNamesCarryTheirModulePrefix(t *testing.T) {
	owned := map[string][]string{
		"auth_":        {usersCollection, oauthProvidersCollection, refreshTokensCollection, gitShortcutsCollection, userSecretsCollection},
		"admin_":       {agentTypesCollection, agentTypeOverridesCollection, modelsCollection, modelProvidersCollection, auditLogCollection, builtinToolsCollection, mcpServersCollection, skillsCollection, userSkillAcksCollection, sessionHeadPromptsCollection},
		"portal_":      {agentsCollection, agentSessionsCollection, agentGroupsCollection, bindingAgentsCollection},
		"bus_":         {inboundQueueCollection, outboundQueueCollection, wechatKFSyncStateCollection},
		"provisioner_": {containerRequestsCollection, runnersCollection},
		"sys_":         {systemFlagsCollection, countersCollection, migrationsCollection},
	}
	for prefix, names := range owned {
		for _, name := range names {
			if !strings.HasPrefix(name, prefix) {
				t.Errorf("%q does not start with %q", name, prefix)
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
	prefixes := []string{"auth_", "admin_", "portal_", "bus_", "provisioner_", "sys_"}
	for name, body := range manifests {
		var manifest struct {
			Collections map[string]json.RawMessage `json:"collections"`
		}
		if err := json.Unmarshal([]byte(body), &manifest); err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		for collection := range manifest.Collections {
			found := false
			for _, prefix := range prefixes {
				if strings.HasPrefix(collection, prefix) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: collection %q has no module prefix", name, collection)
			}
		}
	}
}
