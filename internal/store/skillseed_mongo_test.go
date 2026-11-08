package store_test

// `nanobus skills seed` runs on every boot, so what it must be proven not to do
// is downgrade content, hijack a name that is not its own, or lose the state and
// acknowledgements attached to a skill it does own. Those decisions are made
// against the stored document, hence a real store.

import (
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func bundledSkill(name, body string, version int) nanoprotocol.SkillDef {
	return nanoprotocol.SkillDef{
		Name: name, Description: "desc",
		Files:   map[string]string{"SKILL.md": body},
		Version: version, ContentHash: "hash-" + body,
	}
}

// seedBundledSkill writes the document the seeder would have written, for a test
// that starts from an already-seeded catalog.
func seedBundledSkill(t *testing.T, s *store.Store, name, body string, version int) string {
	t.Helper()
	id := store.BundledSkillID(name)
	if err := s.CreateSkill(store.SkillRecord{
		Capability: store.Capability{
			ID: id, Scope: "platform", Name: name, DefaultEnabled: true,
		},
		Files:            map[string]string{"SKILL.md": body},
		Origin:           "bundled",
		Status:           "published",
		Version:          version,
		PublishedVersion: version,
		ContentHash:      "hash-" + body,
		CreatedBy:        "nanobus",
	}); err != nil {
		t.Fatalf("seed bundled skill %s: %v", name, err)
	}
	return id
}

func TestSeedPlatformSkills_InsertsNewOnes(t *testing.T) {
	s := storetest.Connect(t)

	inserted, updated, err := s.SeedPlatformSkills([]nanoprotocol.SkillDef{bundledSkill("weather", "body", 1)})
	if err != nil {
		t.Fatalf("SeedPlatformSkills: %v", err)
	}
	if inserted != 1 || updated != 0 {
		t.Fatalf("expected 1 insert / 0 updates, got %d / %d", inserted, updated)
	}

	rec, err := s.GetSkill(store.BundledSkillID("weather"))
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if rec.Origin != "bundled" || rec.Version != 1 || rec.Files["SKILL.md"] != "body" {
		t.Fatalf("unexpected skill: %+v", rec)
	}
	// A second run with the same content changes nothing.
	inserted, updated, err = s.SeedPlatformSkills([]nanoprotocol.SkillDef{bundledSkill("weather", "body", 1)})
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if inserted != 0 || updated != 0 {
		t.Fatalf("the seed is not idempotent: %d / %d", inserted, updated)
	}
}

func TestSeedPlatformSkills_UpdatesNewerContent(t *testing.T) {
	s := storetest.Connect(t)
	seedBundledSkill(t, s, "weather", "old", 1)

	inserted, updated, err := s.SeedPlatformSkills([]nanoprotocol.SkillDef{bundledSkill("weather", "new", 2)})
	if err != nil {
		t.Fatalf("SeedPlatformSkills: %v", err)
	}
	if inserted != 0 || updated != 1 {
		t.Fatalf("expected 0 inserts / 1 update, got %d / %d", inserted, updated)
	}

	rec, err := s.GetSkill(store.BundledSkillID("weather"))
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if rec.Version != 2 || rec.PublishedVersion != 2 || rec.Files["SKILL.md"] != "new" {
		t.Fatalf("content not replaced: %+v", rec)
	}
}

// TestSeedPlatformSkills_IgnoresAnOlderVersion: a stale binary must not be able
// to downgrade a catalog.
func TestSeedPlatformSkills_IgnoresAnOlderVersion(t *testing.T) {
	s := storetest.Connect(t)
	id := seedBundledSkill(t, s, "weather", "new", 5)

	inserted, updated, err := s.SeedPlatformSkills([]nanoprotocol.SkillDef{bundledSkill("weather", "old", 1)})
	if err != nil {
		t.Fatalf("SeedPlatformSkills: %v", err)
	}
	if inserted != 0 || updated != 0 {
		t.Fatalf("an older version was not ignored: %d / %d", inserted, updated)
	}
	rec, err := s.GetSkill(id)
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if rec.Version != 5 || rec.Files["SKILL.md"] != "new" {
		t.Fatalf("the catalog was downgraded: %+v", rec)
	}
}

// TestSeedPlatformSkills_NeverHijacksAName: the bundle may only ever write its
// own id, so a skill a human made with the same name is left alone.
func TestSeedPlatformSkills_NeverHijacksAName(t *testing.T) {
	s := storetest.Connect(t)

	// A platform skill (made by an admin, origin "human") claims the name.
	if err := s.CreateSkill(store.SkillRecord{
		Capability: store.Capability{ID: "skill:human", Scope: "platform", Name: "weather", DefaultEnabled: true},
		Files:      map[string]string{"SKILL.md": "an admin's own"},
		Origin:     "human",
	}); err != nil {
		t.Fatalf("CreateSkill: %v", err)
	}

	inserted, updated, err := s.SeedPlatformSkills([]nanoprotocol.SkillDef{bundledSkill("weather", "bundled", 1)})
	if err != nil {
		t.Fatalf("SeedPlatformSkills: %v", err)
	}
	if inserted != 0 || updated != 0 {
		t.Fatalf("the bundle hijacked a name it does not own: %d / %d", inserted, updated)
	}

	rec, err := s.GetSkill("skill:human")
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if rec.Files["SKILL.md"] != "an admin's own" {
		t.Fatalf("the admin's skill was rewritten: %+v", rec)
	}
	if _, err := s.GetSkill(store.BundledSkillID("weather")); err == nil {
		t.Fatal("the bundle created a second skill under a taken name")
	}
}

// TestSeedPlatformSkills_KeepsStateAndAcks: updating content must not reset what
// admins and users attached to the skill — the SQL only ever wrote the content
// columns, and this is the property that keeps a version bump from re-enabling a
// skill someone switched off.
func TestSeedPlatformSkills_KeepsStateAndAcks(t *testing.T) {
	s := storetest.Connect(t)
	id := seedBundledSkill(t, s, "weather", "old", 1)

	off := false
	if err := s.UpdateSkillState(id, &off, nil); err != nil {
		t.Fatalf("UpdateSkillState: %v", err)
	}
	if err := s.AckSkill("u-1", id, 1); err != nil {
		t.Fatalf("AckSkill: %v", err)
	}

	if _, updated, err := s.SeedPlatformSkills([]nanoprotocol.SkillDef{bundledSkill("weather", "new", 2)}); err != nil {
		t.Fatalf("SeedPlatformSkills: %v", err)
	} else if updated != 1 {
		t.Fatalf("expected one update, got %d", updated)
	}

	rec, err := s.GetSkill(id)
	if err != nil {
		t.Fatalf("GetSkill: %v", err)
	}
	if rec.DefaultEnabled {
		t.Fatalf("the seed re-enabled a skill an admin switched off: %+v", rec)
	}
	if version, err := s.SkillAckVersion("u-1", id); err != nil || version != 1 {
		t.Fatalf("the user's acknowledgement was lost: %d (err %v)", version, err)
	}
}

func TestSeedPlatformSkills_IgnoresEmptyEntries(t *testing.T) {
	s := storetest.Connect(t)

	inserted, updated, err := s.SeedPlatformSkills([]nanoprotocol.SkillDef{
		{Name: "", Files: map[string]string{"SKILL.md": "x"}},
		{Name: "no-files"},
	})
	if err != nil {
		t.Fatalf("SeedPlatformSkills: %v", err)
	}
	if inserted != 0 || updated != 0 {
		t.Fatalf("an unusable entry was seeded: %d / %d", inserted, updated)
	}
}
