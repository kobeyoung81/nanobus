package store

import (
	"testing"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// TestEffectivePermissionsOverrideWinsLast pins the last-match-wins order:
// baseline, then per-capability rules, then the user override last. A
// capability 'ask' must not defeat a persisted override 'allow'.
func TestEffectivePermissionsOverrideWinsLast(t *testing.T) {
	at := &nanoprotocol.AgentTypeDef{
		Permissions: []nanoprotocol.PermissionRule{
			{Pattern: "*", Action: nanoprotocol.PermissionAllow},
			{Pattern: "bash", Action: nanoprotocol.PermissionAsk},
		},
		Capabilities: []nanoprotocol.TypeCapabilityBinding{
			{Permissions: []nanoprotocol.PermissionRule{{Pattern: "bash", Action: nanoprotocol.PermissionAsk}}},
		},
	}
	ov := &nanoprotocol.AgentTypeOverride{
		Permissions: []nanoprotocol.PermissionRule{{Pattern: "bash", Action: nanoprotocol.PermissionAllow}},
	}

	rules := effectivePermissions(at, ov)
	if got := nanoprotocol.EffectivePermission("bash", rules); got != nanoprotocol.PermissionAllow {
		t.Fatalf("override should win: got %q", got)
	}
	if got := nanoprotocol.EffectivePermission("read", rules); got != nanoprotocol.PermissionAllow {
		t.Fatalf("baseline default should apply: got %q", got)
	}

	// Without the override the capability ask stands.
	if got := nanoprotocol.EffectivePermission("bash", effectivePermissions(at, nil)); got != nanoprotocol.PermissionAsk {
		t.Fatalf("capability ask expected without override: got %q", got)
	}
}
