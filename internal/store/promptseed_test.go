package store_test

// The prompt seeder runs on every boot against every type, so what it must be
// proven not to do is overwrite an admin's edit — or lose one while believing it
// was a platform seed. Those decisions are made about a document's current
// prompt and its recorded baseline, so they are exercised against a real store.

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/promptbundle"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func promptFixture(id, text string, version int) promptbundle.BundledPrompt {
	return promptbundle.BundledPrompt{ID: id, Version: version, Text: text, ContentHash: promptbundle.ContentHash(text)}
}

// seedType writes an agent type document directly, since SaveAgentType is not
// what this seeder reacts to.
func seedType(t *testing.T, s *store.Store, id, promptText, baselineHash string) {
	t.Helper()
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: "name", Value: id},
		{Key: "strategy", Value: "react"},
		{Key: "context", Value: "windowed"},
		{Key: "output", Value: "stream"},
		{Key: "session_binding", Value: "shared"},
	}
	if promptText != "" {
		doc = append(doc, bson.E{Key: "prompt", Value: promptText})
	}
	if baselineHash != "" {
		doc = append(doc, bson.E{Key: "prompt_hash", Value: baselineHash})
	}
	if _, err := s.DB().Collection("admin_agent_types").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed type %s: %v", id, err)
	}
}

func readType(t *testing.T, s *store.Store, id string) (string, string) {
	t.Helper()
	var doc struct {
		Prompt     string `bson:"prompt"`
		PromptHash string `bson:"prompt_hash"`
	}
	if err := s.DB().Collection("admin_agent_types").
		FindOne(context.Background(), bson.D{{Key: "_id", Value: id}}).Decode(&doc); err != nil {
		t.Fatalf("read type %s: %v", id, err)
	}
	return doc.Prompt, doc.PromptHash
}

// TestSeedPlatformPrompts_FillsEmptyPrompt covers a type that has never had a
// prompt: the bundle seeds it, and records the baseline so the next run knows it
// is a seed.
func TestSeedPlatformPrompts_FillsEmptyPrompt(t *testing.T) {
	s := storetest.Connect(t)
	seedType(t, s, "build", "", "")

	updated, skipped, err := s.SeedPlatformPrompts([]promptbundle.BundledPrompt{promptFixture("build", "bundled text", 1)}, false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if updated != 1 || skipped != 0 {
		t.Fatalf("updated=%d skipped=%d, want 1/0", updated, skipped)
	}
	text, hash := readType(t, s, "build")
	if text != "bundled text" {
		t.Fatalf("prompt not seeded: %q", text)
	}
	if hash != promptbundle.ContentHash("bundled text") {
		t.Fatalf("baseline not restamped: %q", hash)
	}
}

// TestSeedPlatformPrompts_ReplacesUnmodifiedSeed covers the normal upgrade path:
// the stored text still hashes to the recorded baseline, so the new bundled text
// wins.
func TestSeedPlatformPrompts_ReplacesUnmodifiedSeed(t *testing.T) {
	s := storetest.Connect(t)
	old := "old platform prompt"
	seedType(t, s, "build", old, promptbundle.ContentHash(old))

	updated, skipped, err := s.SeedPlatformPrompts([]promptbundle.BundledPrompt{promptFixture("build", "new platform prompt", 2)}, false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if updated != 1 || skipped != 0 {
		t.Fatalf("updated=%d skipped=%d, want 1/0", updated, skipped)
	}
	if text, _ := readType(t, s, "build"); text != "new platform prompt" {
		t.Fatalf("unmodified seed not replaced: %q", text)
	}
}

// TestSeedPlatformPrompts_KeepsAdminEdit is the safety property: a prompt edited
// after seeding is never clobbered (and --force overrides that).
func TestSeedPlatformPrompts_KeepsAdminEdit(t *testing.T) {
	s := storetest.Connect(t)
	seed := "seeded prompt"
	edited := "seeded prompt, with an admin tweak"
	seedType(t, s, "build", edited, promptbundle.ContentHash(seed))

	updated, skipped, err := s.SeedPlatformPrompts([]promptbundle.BundledPrompt{promptFixture("build", "new prompt", 2)}, false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if updated != 0 || skipped != 1 {
		t.Fatalf("updated=%d skipped=%d, want 0/1", updated, skipped)
	}
	if text, _ := readType(t, s, "build"); text != edited {
		t.Fatalf("the admin edit was clobbered: %q", text)
	}

	// --force overrides the local edit.
	if updated, _, err = s.SeedPlatformPrompts([]promptbundle.BundledPrompt{promptFixture("build", "new prompt", 2)}, true); err != nil {
		t.Fatalf("forced seed: %v", err)
	}
	if updated != 1 {
		t.Fatalf("forced updated=%d, want 1", updated)
	}
	if text, _ := readType(t, s, "build"); text != "new prompt" {
		t.Fatalf("forced seed did not write: %q", text)
	}
}

// TestSeedPlatformPrompts_SkipsUnmarkedNonEmpty covers pre-bundle databases that
// migration 070 has not stamped: they are treated as admin-owned.
func TestSeedPlatformPrompts_SkipsUnmarkedNonEmpty(t *testing.T) {
	s := storetest.Connect(t)
	seedType(t, s, "build", "hand written", "")

	updated, skipped, err := s.SeedPlatformPrompts([]promptbundle.BundledPrompt{promptFixture("build", "bundled", 1)}, false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if updated != 0 || skipped != 1 {
		t.Fatalf("updated=%d skipped=%d, want 0/1", updated, skipped)
	}
	if text, _ := readType(t, s, "build"); text != "hand written" {
		t.Fatalf("unmarked prompt was overwritten: %q", text)
	}
}

// TestSeedPlatformPrompts_IdempotentAndUnknownType covers the no-op case and a
// type that does not exist (the bundle must never create types).
func TestSeedPlatformPrompts_IdempotentAndUnknownType(t *testing.T) {
	s := storetest.Connect(t)
	text := "bundled text"
	seedType(t, s, "build", text, promptbundle.ContentHash(text))

	updated, skipped, err := s.SeedPlatformPrompts([]promptbundle.BundledPrompt{
		promptFixture("build", text, 1),
		promptFixture("ghost", "whatever", 1),
	}, false)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if updated != 0 || skipped != 2 {
		t.Fatalf("updated=%d skipped=%d, want 0/2", updated, skipped)
	}
	n, err := s.DB().Collection("admin_agent_types").CountDocuments(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("count types: %v", err)
	}
	if n != 1 {
		t.Fatalf("the bundle created a type: %d documents", n)
	}
}

// TestSeedPlatformPrompts_KeepsTheOtherSettings pins the half a seeder is most
// likely to break: writing a prompt must not disturb the runtime knobs beside it.
func TestSeedPlatformPrompts_KeepsTheOtherSettings(t *testing.T) {
	// A save validates the permission rules against the catalog, so it has to be
	// there first.
	s := storetest.Connect(t)
	seedPermissionCatalog(t, s)
	if err := s.SaveAgentType(agentTypeFixture("build", map[string]string{"session_mode": "continuous"})); err != nil {
		t.Fatalf("SaveAgentType: %v", err)
	}

	if _, _, err := s.SeedPlatformPrompts([]promptbundle.BundledPrompt{promptFixture("build", "bundled text", 1)}, false); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := s.GetAgentType("build")
	if err != nil {
		t.Fatalf("GetAgentType: %v", err)
	}
	if got.Settings["session_mode"] != "continuous" {
		t.Fatalf("the seed dropped the type's settings: %+v", got.Settings)
	}
}
