package store_test

// The shared head prompt: build and plan read one document, so the properties
// under test are the identity of that document (scope+owner, never a mode) and
// the edit guard the seeder must respect. Both are about stored documents, so
// they run against a real store.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/promptbundle"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// seedHead writes a head document directly, since the seeder is not what these
// tests react to.
func seedHead(t *testing.T, s *store.Store, scope, ownerID, content, baselineHash string) {
	t.Helper()
	now := time.Now().UTC()
	doc := bson.D{
		{Key: "scope", Value: scope},
		{Key: "owner_id", Value: ownerID},
		{Key: "content", Value: content},
		{Key: "baseline_hash", Value: baselineHash},
		{Key: "baseline_version", Value: 0},
		{Key: "updated_at", Value: now},
	}
	if _, err := s.DB().Collection("admin_prompts").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed head %s/%s: %v", scope, ownerID, err)
	}
}

func readHead(t *testing.T, s *store.Store, scope, ownerID string) (string, string, time.Time) {
	t.Helper()
	var doc struct {
		Content      string    `bson:"content"`
		BaselineHash string    `bson:"baseline_hash"`
		UpdatedAt    time.Time `bson:"updated_at"`
	}
	if err := s.DB().Collection("admin_prompts").
		FindOne(context.Background(), bson.D{{Key: "scope", Value: scope}, {Key: "owner_id", Value: ownerID}}).
		Decode(&doc); err != nil {
		t.Fatalf("read head %s/%s: %v", scope, ownerID, err)
	}
	return doc.Content, doc.BaselineHash, doc.UpdatedAt
}

// TestEffectiveSessionHeadPrefersAgentOverride: an agent document wins.
func TestEffectiveSessionHeadPrefersAgentOverride(t *testing.T) {
	s := storetest.Connect(t)
	seedHead(t, s, store.PromptScopePlatform, "", "platform head", "")
	seedHead(t, s, store.PromptScopeAgent, "a1", "agent head", "")

	got, err := s.EffectiveSessionHead("a1")
	if err != nil {
		t.Fatalf("EffectiveSessionHead: %v", err)
	}
	if got != "agent head" {
		t.Fatalf("head = %q, want the agent override", got)
	}
}

// TestEffectiveSessionHeadFallsBackToPlatform covers an agent with no override,
// and the empty store a fresh deployment starts from.
func TestEffectiveSessionHeadFallsBackToPlatform(t *testing.T) {
	s := storetest.Connect(t)

	// Nothing stored at all: the caller keeps its built-in default.
	if got, err := s.EffectiveSessionHead("a1"); err != nil || got != "" {
		t.Fatalf("head = %q err = %v, want an empty head", got, err)
	}

	seedHead(t, s, store.PromptScopePlatform, "", "platform head", "h")
	if got, err := s.EffectiveSessionHead("a1"); err != nil || got != "platform head" {
		t.Fatalf("head = %q err = %v, want the platform value", got, err)
	}

	// An agent document with empty content falls back rather than shadowing.
	seedHead(t, s, store.PromptScopeAgent, "a1", "", "")
	if got, err := s.EffectiveSessionHead("a1"); err != nil || got != "platform head" {
		t.Fatalf("an empty override shadowed the platform head: %q err = %v", got, err)
	}
}

// TestSaveSessionHead_RestampsThePlatformBaseline pins what an admin save does
// besides storing text: the platform row records the hash of what it now holds
// (that is what makes the next seed a no-op) and restamps updated_at, which the
// console shows.
func TestSaveSessionHead_RestampsThePlatformBaseline(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.SaveSessionHead(store.PromptScopePlatform, "", "hand written head"); err != nil {
		t.Fatalf("SaveSessionHead: %v", err)
	}
	content, hash, updatedAt := readHead(t, s, store.PromptScopePlatform, "")
	if content != "hand written head" {
		t.Fatalf("content = %q", content)
	}
	if hash != promptbundle.ContentHash("hand written head") {
		t.Fatalf("the platform save did not restamp the baseline: %q", hash)
	}
	if updatedAt.IsZero() || time.Since(updatedAt) > time.Minute {
		t.Fatalf("updated_at = %v, want just now", updatedAt)
	}

	// An agent-scoped save keeps an empty baseline: it is not a platform seed and
	// must never be mistaken for one.
	if err := s.SaveSessionHead(store.PromptScopeAgent, "a1", "agent head"); err != nil {
		t.Fatalf("SaveSessionHead(agent): %v", err)
	}
	if _, hash, _ := readHead(t, s, store.PromptScopeAgent, "a1"); hash != "" {
		t.Fatalf("an agent save recorded a baseline: %q", hash)
	}
}

// TestSaveSessionHead_RejectsEmptyAndUnknownScope keeps the two inputs the SQL
// path rejected.
func TestSaveSessionHead_RejectsEmptyAndUnknownScope(t *testing.T) {
	s := storetest.Connect(t)
	if err := s.SaveSessionHead(store.PromptScopePlatform, "", ""); err == nil {
		t.Fatal("an empty head was accepted")
	}
	if err := s.SaveSessionHead("mode", "", "text"); err == nil {
		t.Fatal("an unknown scope was accepted")
	}
	n, err := s.DB().Collection("admin_prompts").CountDocuments(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("a rejected save wrote %d document(s)", n)
	}
}

// TestSessionHead_IdentityIsScopeAndOwner is the property the whole design rests
// on: the key has no mode in it, so build and plan cannot hold different heads.
// The unique index is what enforces that, so a duplicate is rejected by the
// server rather than merged away.
func TestSessionHead_IdentityIsScopeAndOwner(t *testing.T) {
	s := storetest.Connect(t)
	seedHead(t, s, store.PromptScopePlatform, "", "one head", "")

	_, err := s.DB().Collection("admin_prompts").InsertOne(context.Background(), bson.D{
		{Key: "scope", Value: store.PromptScopePlatform},
		{Key: "owner_id", Value: ""},
		{Key: "content", Value: "a second head"},
	})
	if err == nil {
		t.Fatal("the unique index did not reject a second platform head")
	}
	if got, err := s.EffectiveSessionHead("a1"); err != nil || got != "one head" {
		t.Fatalf("head = %q err = %v", got, err)
	}
}

// TestSeedSessionHead_InsertsWhenMissing seeds an empty collection.
func TestSeedSessionHead_InsertsWhenMissing(t *testing.T) {
	s := storetest.Connect(t)

	updated, err := s.SeedSessionHead(headFixture("bundled head", 1), false)
	if err != nil {
		t.Fatalf("SeedSessionHead: %v", err)
	}
	if !updated {
		t.Fatal("expected the empty collection to be seeded")
	}
	content, hash, _ := readHead(t, s, store.PromptScopePlatform, "")
	if content != "bundled head" || hash != promptbundle.ContentHash("bundled head") {
		t.Fatalf("content = %q hash = %q", content, hash)
	}
}

// TestSeedSessionHead_KeepsAdminEdit is the safety property: an edited platform
// head is never clobbered by a deploy.
func TestSeedSessionHead_KeepsAdminEdit(t *testing.T) {
	s := storetest.Connect(t)
	edited := "hand written"
	seedHead(t, s, store.PromptScopePlatform, "", edited, promptbundle.ContentHash("old bundled"))

	updated, err := s.SeedSessionHead(headFixture("new bundled", 1), false)
	if err != nil {
		t.Fatalf("SeedSessionHead: %v", err)
	}
	if updated {
		t.Fatal("an admin-edited head must be kept")
	}
	if content, _, _ := readHead(t, s, store.PromptScopePlatform, ""); content != edited {
		t.Fatalf("the admin edit was clobbered: %q", content)
	}

	// --force overrides the edit guard.
	if updated, err = s.SeedSessionHead(headFixture("new bundled", 1), true); err != nil {
		t.Fatalf("forced seed: %v", err)
	}
	if !updated {
		t.Fatal("a forced seed did not write")
	}
	if content, _, _ := readHead(t, s, store.PromptScopePlatform, ""); content != "new bundled" {
		t.Fatalf("forced seed wrote %q", content)
	}
}

// TestSeedSessionHead_ReplacesUnmodifiedSeed is the normal upgrade path.
func TestSeedSessionHead_ReplacesUnmodifiedSeed(t *testing.T) {
	s := storetest.Connect(t)
	old := "old bundled"
	seedHead(t, s, store.PromptScopePlatform, "", old, promptbundle.ContentHash(old))

	updated, err := s.SeedSessionHead(headFixture("new bundled", 2), false)
	if err != nil {
		t.Fatalf("SeedSessionHead: %v", err)
	}
	if !updated {
		t.Fatal("an unmodified seed must be replaced")
	}
	if content, _, _ := readHead(t, s, store.PromptScopePlatform, ""); content != "new bundled" {
		t.Fatalf("content = %q", content)
	}
	n, err := s.DB().Collection("admin_prompts").CountDocuments(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("the seed appended a document instead of upserting: %d", n)
	}
}

func headFixture(text string, version int) promptbundle.BundledPrompt {
	return promptbundle.BundledPrompt{ID: "common", Version: version, Text: text, ContentHash: promptbundle.ContentHash(text)}
}
