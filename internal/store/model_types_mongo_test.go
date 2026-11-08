package store_test

// Giving one platform model to every built-in agent type: the console action a
// fresh installation needs, because nothing binds the first model to the types
// that exist before anyone edits them. The action is fill-only — an
// administrator's per-type defaults survive it — and idempotent.

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func seedModel(t *testing.T, s *store.Store, scope, name string) int64 {
	t.Helper()
	providerID, err := s.CreateModelProvider(nanoprotocol.ModelProviderDef{
		Scope: scope, Name: "p-" + name, BaseURL: "http://example.test",
	})
	if err != nil {
		t.Fatalf("CreateModelProvider: %v", err)
	}
	id, err := s.CreateModel(nanoprotocol.ModelDef{
		ProviderID: providerID, Scope: scope, Name: name, Model: name,
	})
	if err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	return id
}

// seedModelType inserts a type document directly: `seeded` is written by the
// catalog seeder and deliberately never by SaveAgentType, so it is the one field
// an API-level fixture cannot set.
func seedModelType(t *testing.T, s *store.Store, id string, seeded bool, models []nanoprotocol.TypeModelBinding) {
	t.Helper()
	stored := make([]bson.D, 0, len(models))
	for _, m := range models {
		stored = append(stored, bson.D{
			{Key: "model_id", Value: m.ModelID},
			{Key: "is_default", Value: m.IsDefault},
			{Key: "locked", Value: m.Locked},
		})
	}
	doc := bson.D{
		{Key: "_id", Value: id},
		{Key: "name", Value: id},
		{Key: "seeded", Value: seeded},
		{Key: "strategy", Value: "react"},
		{Key: "context", Value: "windowed"},
		{Key: "output", Value: "stream"},
		{Key: "session_binding", Value: "shared"},
	}
	if len(stored) > 0 {
		doc = append(doc, bson.E{Key: "models", Value: stored})
	}
	if _, err := s.DB().Collection("agent_types").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed type %s: %v", id, err)
	}
}

func TestApplyModelToBuiltInTypes_FillsEmptyDefaultsOnly(t *testing.T) {
	s := storetest.Connect(t)
	oldModel := seedModel(t, s, "platform", "basic")
	model := seedModel(t, s, "platform", "pro")

	seedModelType(t, s, "build", true, nil)
	seedModelType(t, s, "plan", true, []nanoprotocol.TypeModelBinding{{ModelID: oldModel, IsDefault: true}})
	seedModelType(t, s, "custom", false, nil)

	applied, already, defaults, err := s.ApplyModelToBuiltInTypes(model)
	if err != nil {
		t.Fatalf("ApplyModelToBuiltInTypes: %v", err)
	}
	if len(applied) != 2 || applied[0] != "build" || applied[1] != "plan" {
		t.Fatalf("applied = %v; want [build plan]", applied)
	}
	if len(already) != 0 {
		t.Fatalf("already = %v; want none", already)
	}
	if len(defaults) != 1 || defaults[0] != "build" {
		t.Fatalf("defaults = %v; want [build]", defaults)
	}

	build, err := s.GetAgentType("build")
	if err != nil || build == nil {
		t.Fatalf("GetAgentType(build): %v", err)
	}
	if len(build.Models) != 1 || build.Models[0].ModelID != model || !build.Models[0].IsDefault {
		t.Fatalf("build models = %+v; want the applied model as default", build.Models)
	}
	plan, err := s.GetAgentType("plan")
	if err != nil || plan == nil {
		t.Fatalf("GetAgentType(plan): %v", err)
	}
	if len(plan.Models) != 2 {
		t.Fatalf("plan models = %+v; want the old default plus the new model", plan.Models)
	}
	for _, binding := range plan.Models {
		if binding.ModelID == model && binding.IsDefault {
			t.Fatalf("plan's existing default was replaced: %+v", plan.Models)
		}
		if binding.ModelID == oldModel && !binding.IsDefault {
			t.Fatalf("plan's old default lost its flag: %+v", plan.Models)
		}
	}
	custom, err := s.GetAgentType("custom")
	if err != nil || custom == nil {
		t.Fatalf("GetAgentType(custom): %v", err)
	}
	if len(custom.Models) != 0 {
		t.Fatalf("custom type was touched: %+v", custom.Models)
	}
}

func TestApplyModelToBuiltInTypes_IsIdempotent(t *testing.T) {
	s := storetest.Connect(t)
	model := seedModel(t, s, "platform", "pro")
	seedModelType(t, s, "build", true, nil)

	if _, _, _, err := s.ApplyModelToBuiltInTypes(model); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	applied, already, defaults, err := s.ApplyModelToBuiltInTypes(model)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(applied) != 0 || len(defaults) != 0 {
		t.Fatalf("second apply changed something: applied=%v defaults=%v", applied, defaults)
	}
	if len(already) != 1 || already[0] != "build" {
		t.Fatalf("already = %v; want [build]", already)
	}
}

func TestApplyModelToBuiltInTypes_RefusesNonPlatformModels(t *testing.T) {
	s := storetest.Connect(t)
	model := seedModel(t, s, "user", "personal")

	if _, _, _, err := s.ApplyModelToBuiltInTypes(model); !errors.Is(err, store.ErrNotPlatformModel) {
		t.Fatalf("err = %v; want ErrNotPlatformModel", err)
	}
	if _, _, _, err := s.ApplyModelToBuiltInTypes(99999); !errors.Is(err, store.ErrModelNotFound) {
		t.Fatalf("err = %v; want ErrModelNotFound", err)
	}
}
