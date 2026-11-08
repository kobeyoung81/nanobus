package store_test

// The model catalog is two collections with a foreign key between them, so what
// these tests pin includes the two things the move had to re-create by hand: the
// unique keys (a model name is unique under its provider) and the cascade (a
// provider's models go with it). They run against a real server, because both are
// properties of the server, not of the call shape.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func TestCreateModelProvider_AssignsIncreasingIDs(t *testing.T) {
	s := storetest.Connect(t)

	first, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{
		Scope: "platform", Name: "OpenAI", BaseURL: "https://api.openai.com", APIKey: "sk-1",
	})
	if err != nil {
		t.Fatalf("CreateModelProvider: %v", err)
	}
	second, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{
		Scope: "user", OwnerUserID: "u-1", Name: "Local", Kind: "openai", BaseURL: "http://localhost:1",
	})
	if err != nil {
		t.Fatalf("CreateModelProvider: %v", err)
	}
	if first == 0 || second <= first {
		t.Fatalf("ids are not increasing: %d %d", first, second)
	}

	got, err := s.GetModelProvider(first)
	if err != nil {
		t.Fatalf("GetModelProvider: %v", err)
	}
	if got == nil {
		t.Fatal("the provider was not stored")
	}
	if got.Name != "OpenAI" || got.BaseURL != "https://api.openai.com" || got.APIKey != "sk-1" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	// An unset kind defaults to openai, as the column did.
	if got.Kind != "openai" {
		t.Fatalf("kind = %q, want openai", got.Kind)
	}
	// The timestamps are formatted strings on the wire (DATE_FORMAT in SQL).
	if len(got.CreatedAt) != len("2006-01-02T15:04:05") || got.CreatedAt != got.UpdatedAt {
		t.Fatalf("unexpected timestamps: %+v", got)
	}
}

// TestCreateModelProvider_DuplicateNameRefused pins the unique (scope, owner,
// name): two providers of the same name under one owner would make the catalog
// ambiguous, and the SQL refused them.
func TestCreateModelProvider_DuplicateNameRefused(t *testing.T) {
	s := storetest.Connect(t)

	first := nanoprotocol.ModelProviderDef{Scope: "user", OwnerUserID: "u-1", Name: "Same", BaseURL: "http://x"}
	if _, err := s.CreateModelProvider(first); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := s.CreateModelProvider(first); err == nil {
		t.Fatal("a duplicate (scope, owner, name) was accepted: the unique index is missing")
	}
	// The same name under another owner is a different provider.
	other := first
	other.OwnerUserID = "u-2"
	if _, err := s.CreateModelProvider(other); err != nil {
		t.Fatalf("another owner's provider was refused: %v", err)
	}
}

func TestListModelProviders_ScopeAndOrder(t *testing.T) {
	s := storetest.Connect(t)

	for _, p := range []nanoprotocol.ModelProviderDef{
		{Scope: "platform", Name: "Zeta", BaseURL: "http://z"},
		{Scope: "platform", Name: "Alpha", BaseURL: "http://a"},
		{Scope: "user", OwnerUserID: "u-1", Name: "Beta", BaseURL: "http://b"},
		{Scope: "user", OwnerUserID: "u-2", Name: "Hidden", BaseURL: "http://h"},
	} {
		if _, err := s.CreateModelProvider(p); err != nil {
			t.Fatalf("CreateModelProvider(%s): %v", p.Name, err)
		}
	}

	providers, err := s.ListModelProviders("u-1")
	if err != nil {
		t.Fatalf("ListModelProviders: %v", err)
	}
	// Platform rows first (scope ASC), each group by name, and another user's
	// provider is nowhere in it.
	names := make([]string, 0, len(providers))
	for _, p := range providers {
		names = append(names, p.Name)
	}
	want := []string{"Alpha", "Zeta", "Beta"}
	if len(names) != len(want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, names)
		}
	}
}

func TestGetModelProvider_MissingIsNil(t *testing.T) {
	s := storetest.Connect(t)

	got, err := s.GetModelProvider(4242)
	if err != nil {
		t.Fatalf("GetModelProvider: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

// TestUpdateModelProvider_KeepsTheStoredKey: the admin UI never receives the key
// back, so an empty one on the way in means "keep what is stored".
func TestUpdateModelProvider_KeepsTheStoredKey(t *testing.T) {
	s := storetest.Connect(t)

	id, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{
		Scope: "platform", Name: "OpenAI", BaseURL: "http://old", APIKey: "sk-stored",
	})
	if err != nil {
		t.Fatalf("CreateModelProvider: %v", err)
	}

	// BSON keeps milliseconds, so two writes in the same tick look identical; the
	// assertion below is about updated_at having moved at all.
	time.Sleep(2 * time.Millisecond)
	if err := s.UpdateModelProvider(id, "", nanoprotocol.ModelProviderDef{
		Name: "OpenAI", Kind: "anthropic", BaseURL: "http://new",
	}); err != nil {
		t.Fatalf("UpdateModelProvider: %v", err)
	}

	got, err := s.GetModelProvider(id)
	if err != nil {
		t.Fatalf("GetModelProvider: %v", err)
	}
	if got.BaseURL != "http://new" || got.Kind != "anthropic" {
		t.Fatalf("the update did not land: %+v", got)
	}
	if got.APIKey != "sk-stored" {
		t.Fatalf("an empty key wiped the stored one: %+v", got)
	}
	// Compared on the stored times, not the formatted strings: both are rendered
	// to the second (as DATE_FORMAT did), so a fast update looks unchanged.
	var doc struct {
		CreatedAt time.Time `bson:"created_at"`
		UpdatedAt time.Time `bson:"updated_at"`
	}
	if err := s.DB().Collection("model_providers").
		FindOne(context.Background(), bson.D{{Key: "_id", Value: id}}).Decode(&doc); err != nil {
		t.Fatalf("read provider: %v", err)
	}
	if !doc.UpdatedAt.After(doc.CreatedAt) {
		t.Fatalf("updated_at did not move: %v → %v", doc.CreatedAt, doc.UpdatedAt)
	}
}

// TestUpdateModelProvider_Ownership: the SQL's `owner_user_id = ? OR
// (scope='platform' AND ? = ”)` — the owner may write their own row, and the
// admin path (an empty owner) may write a platform one, nothing else.
func TestUpdateModelProvider_Ownership(t *testing.T) {
	s := storetest.Connect(t)

	platform, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{Scope: "platform", Name: "P", BaseURL: "http://p"})
	if err != nil {
		t.Fatalf("create platform: %v", err)
	}
	mine, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{Scope: "user", OwnerUserID: "u-1", Name: "M", BaseURL: "http://m"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Another user cannot write either.
	other := nanoprotocol.ModelProviderDef{Name: "X", BaseURL: "http://x"}
	if err := s.UpdateModelProvider(platform, "u-2", other); err == nil {
		t.Fatal("another user updated a platform provider")
	}
	if err := s.UpdateModelProvider(mine, "u-2", other); err == nil {
		t.Fatal("another user updated someone else's provider")
	}
	// The owner can.
	if err := s.UpdateModelProvider(mine, "u-1", nanoprotocol.ModelProviderDef{Name: "M2", BaseURL: "http://m2"}); err != nil {
		t.Fatalf("the owner could not update: %v", err)
	}
	// The admin path can write the platform row.
	if err := s.UpdateModelProvider(platform, "", nanoprotocol.ModelProviderDef{Name: "P2", BaseURL: "http://p2"}); err != nil {
		t.Fatalf("the admin path could not update a platform provider: %v", err)
	}
	// A missing provider is still an error.
	if err := s.UpdateModelProvider(4242, "", other); err == nil {
		t.Fatal("expected an error for a missing provider")
	}
}

// TestDeleteModelProvider_CascadesToModels replaces the foreign key's ON DELETE
// CASCADE: the models go with their provider.
func TestDeleteModelProvider_CascadesToModels(t *testing.T) {
	s := storetest.Connect(t)
	ctx := context.Background()

	provider, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{Scope: "platform", Name: "P", BaseURL: "http://p"})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := s.CreateModel(nanoprotocol.ModelDef{ProviderID: provider, Scope: "platform", Name: "M", Model: "m-1"}); err != nil {
		t.Fatalf("create model: %v", err)
	}

	if err := s.DeleteModelProvider(provider, ""); err != nil {
		t.Fatalf("DeleteModelProvider: %v", err)
	}

	if n, err := s.DB().Collection("models").CountDocuments(ctx, bson.D{}); err != nil || n != 0 {
		t.Fatalf("the models did not go with the provider: %d (err %v)", n, err)
	}
	// And the provider is gone.
	if got, err := s.GetModelProvider(provider); err != nil || got != nil {
		t.Fatalf("the provider survived: %+v (err %v)", got, err)
	}
	// Deleting it again says so.
	if err := s.DeleteModelProvider(provider, ""); err == nil {
		t.Fatal("expected an error for a missing provider")
	}
}

func TestCreateAndListModels(t *testing.T) {
	s := storetest.Connect(t)

	provider, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{Scope: "platform", Name: "P", BaseURL: "http://p"})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	id, err := s.CreateModel(nanoprotocol.ModelDef{
		ProviderID: provider, Scope: "platform", Name: "Sonnet", Model: "claude-sonnet",
		ContextWindow: 200_000, MaxOutputTokens: 8_000, ThinkingEnabled: true,
		ThinkingEffort: "high", Params: `{"temperature":0.2}`,
	})
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	if id == 0 {
		t.Fatal("no id was returned")
	}
	// Another user's model stays out of the list.
	if _, err := s.CreateModel(nanoprotocol.ModelDef{
		ProviderID: provider, Scope: "user", OwnerUserID: "u-2", Name: "Theirs", Model: "x",
	}); err != nil {
		t.Fatalf("create other model: %v", err)
	}

	got, err := s.GetModel(id)
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if got == nil || got.Name != "Sonnet" || got.Model != "claude-sonnet" || got.ContextWindow != 200_000 ||
		got.MaxOutputTokens != 8_000 || !got.ThinkingEnabled || got.ThinkingEffort != "high" ||
		got.Params != `{"temperature":0.2}` {
		t.Fatalf("round trip lost fields: %+v", got)
	}

	models, err := s.ListModels("u-1")
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != id {
		t.Fatalf("expected only the platform model, got %+v", models)
	}

	byProvider, err := s.ListModelsByProvider(provider)
	if err != nil {
		t.Fatalf("ListModelsByProvider: %v", err)
	}
	if len(byProvider) != 2 {
		t.Fatalf("expected the provider's 2 models, got %d", len(byProvider))
	}
	// Sorted by name: theirs before the platform one? No — the sort is (scope,
	// name), and 'platform' < 'user'.
	if byProvider[0].Name != "Sonnet" || byProvider[1].Name != "Theirs" {
		t.Fatalf("unexpected order: %+v", byProvider)
	}

	if missing, err := s.GetModel(4242); err != nil || missing != nil {
		t.Fatalf("expected nil for a missing model, got %+v (err %v)", missing, err)
	}
}

// TestCreateModel_DuplicateNameRefused pins the unique (provider_id, name).
func TestCreateModel_DuplicateNameRefused(t *testing.T) {
	s := storetest.Connect(t)

	provider, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{Scope: "platform", Name: "P", BaseURL: "http://p"})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := s.CreateModel(nanoprotocol.ModelDef{ProviderID: provider, Scope: "platform", Name: "M", Model: "m"}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := s.CreateModel(nanoprotocol.ModelDef{ProviderID: provider, Scope: "platform", Name: "M", Model: "m2"}); err == nil {
		t.Fatal("a duplicate (provider_id, name) was accepted: the unique index is missing")
	}
}

// TestUpdateModel_UnsetsWhatZeroMeans: the SQL's NULLIF(?, 0) / NULLIF(?, ”),
// which is how the admin UI clears a limit or the params.
func TestUpdateModel_UnsetsWhatZeroMeans(t *testing.T) {
	s := storetest.Connect(t)

	provider, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{Scope: "platform", Name: "P", BaseURL: "http://p"})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	id, err := s.CreateModel(nanoprotocol.ModelDef{
		ProviderID: provider, Scope: "platform", Name: "M", Model: "m",
		ContextWindow: 1000, MaxOutputTokens: 100, ThinkingEffort: "high", Params: `{"a":1}`,
		ThinkingEnabled: true,
	})
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}

	if err := s.UpdateModel(id, "", nanoprotocol.ModelDef{
		Name: "M2", Model: "m2", ThinkingEnabled: false, Params: "not-json",
	}); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}

	got, err := s.GetModel(id)
	if err != nil {
		t.Fatalf("GetModel: %v", err)
	}
	if got.Name != "M2" || got.Model != "m2" || got.ThinkingEnabled {
		t.Fatalf("the update did not land: %+v", got)
	}
	if got.ContextWindow != 0 || got.MaxOutputTokens != 0 || got.ThinkingEffort != "" {
		t.Fatalf("zeros did not unset: %+v", got)
	}
	// params is opaque text here: what is stored is what the caller sent, and
	// ValidParamsJSON is what the API layer validates with.
	if got.Params != "not-json" {
		t.Fatalf("params = %q", got.Params)
	}

	// Ownership is enforced as it was for providers.
	if err := s.UpdateModel(id, "u-2", nanoprotocol.ModelDef{Name: "X", Model: "x"}); err == nil {
		t.Fatal("another user updated a platform model")
	}
	if err := s.UpdateModel(4242, "", nanoprotocol.ModelDef{Name: "X", Model: "x"}); err == nil {
		t.Fatal("expected an error for a missing model")
	}
}

func TestDeleteModel(t *testing.T) {
	s := storetest.Connect(t)

	provider, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{Scope: "platform", Name: "P", BaseURL: "http://p"})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	id, err := s.CreateModel(nanoprotocol.ModelDef{ProviderID: provider, Scope: "user", OwnerUserID: "u-1", Name: "M", Model: "m"})
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}

	if err := s.DeleteModel(id, "u-2"); err == nil {
		t.Fatal("another user deleted someone else's model")
	}
	if err := s.DeleteModel(id, "u-1"); err != nil {
		t.Fatalf("the owner could not delete: %v", err)
	}
	if got, err := s.GetModel(id); err != nil || got != nil {
		t.Fatalf("the model survived: %+v (err %v)", got, err)
	}
}

// TestListVisibleModels_JoinsTheProvider replaces the SQL's INNER JOIN with a
// second read, so it has to produce the same two things: the provider's
// connection fields on every model, and no model whose provider is missing.
func TestListVisibleModels_JoinsTheProvider(t *testing.T) {
	s := storetest.Connect(t)
	ctx := context.Background()

	provider, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{
		Scope: "platform", Name: "P", Kind: "anthropic", BaseURL: "http://p", APIKey: "sk-p",
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if _, err := s.CreateModel(nanoprotocol.ModelDef{
		ProviderID: provider, Scope: "platform", Name: "M", Model: "m",
	}); err != nil {
		t.Fatalf("create model: %v", err)
	}
	// A model whose provider is gone: the SQL's inner join hid it, and the
	// runtime could not have used it either.
	orphanID, err := s.CreateModel(nanoprotocol.ModelDef{
		ProviderID: provider, Scope: "platform", Name: "Orphan", Model: "o",
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if _, err := s.DB().Collection("models").UpdateOne(ctx,
		bson.D{{Key: "_id", Value: orphanID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "provider_id", Value: int64(999)}}}},
	); err != nil {
		t.Fatalf("orphan the model: %v", err)
	}

	models, err := s.ListVisibleModels("u-1")
	if err != nil {
		t.Fatalf("ListVisibleModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("expected only the model with a provider, got %+v", models)
	}
	m := models[0]
	if m.ProviderKind != "anthropic" || m.ProviderBaseURL != "http://p" || m.ProviderAPIKey != "sk-p" {
		t.Fatalf("the provider fields were not joined in: %+v", m)
	}
}

func TestListVisibleModels_Empty(t *testing.T) {
	s := storetest.Connect(t)

	models, err := s.ListVisibleModels("u-1")
	if err != nil {
		t.Fatalf("ListVisibleModels: %v", err)
	}
	if len(models) != 0 {
		t.Fatalf("expected no models, got %+v", models)
	}
}

// TestMigrations_DeclareModelIndexes pins the manifest: the two unique keys are
// constraints, and the list queries need the (scope, name) prefixes.
func TestMigrations_DeclareModelIndexes(t *testing.T) {
	s := storetest.Connect(t)

	assertIndexKeys(t, s.DB().Collection("model_providers"), "scope_owner_name_unique", [][2]any{
		{"scope", int32(1)}, {"owner_user_id", int32(1)}, {"name", int32(1)},
	})
	assertIndexUnique(t, s.DB().Collection("model_providers"), "scope_owner_name_unique")
	assertIndexKeys(t, s.DB().Collection("model_providers"), "scope_name", [][2]any{
		{"scope", int32(1)}, {"name", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("model_providers"), "scope_owner_name", [][2]any{
		{"scope", int32(1)}, {"owner_user_id", int32(1)}, {"name", int32(1)},
	})

	assertIndexKeys(t, s.DB().Collection("models"), "provider_name_unique", [][2]any{
		{"provider_id", int32(1)}, {"name", int32(1)},
	})
	assertIndexUnique(t, s.DB().Collection("models"), "provider_name_unique")
	assertIndexKeys(t, s.DB().Collection("models"), "scope_name", [][2]any{
		{"scope", int32(1)}, {"name", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("models"), "scope_owner_name", [][2]any{
		{"scope", int32(1)}, {"owner_user_id", int32(1)}, {"name", int32(1)},
	})
}

// TestValidParamsJSON is the one pure helper left in the domain.
func TestValidParamsJSON(t *testing.T) {
	for _, ok := range []string{"", `{}`, `{"temperature":0.2}`} {
		if !store.ValidParamsJSON(ok) {
			t.Fatalf("ValidParamsJSON(%q) = false", ok)
		}
	}
	for _, bad := range []string{"not-json", `[]`, `"text"`} {
		if store.ValidParamsJSON(bad) {
			t.Fatalf("ValidParamsJSON(%q) = true", bad)
		}
	}
}
