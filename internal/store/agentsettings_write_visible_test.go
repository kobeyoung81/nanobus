package store_test

// The settings write lives on the agent document, which moved to MongoDB, so
// these assert what the document holds afterwards rather than the SQL string the
// code was told to send. They live outside the package because storetest imports
// store and an internal test cannot import it back.

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// agentWithSettings returns a store holding one agent, and a reader for whatever
// settings end up on it.
func agentWithSettings(t *testing.T) (*store.Store, func() map[string]string) {
	t.Helper()
	s := storetest.Connect(t)
	now := time.Now().UTC()
	if _, err := s.DB().Collection("agents").InsertOne(context.Background(), bson.D{
		{Key: "_id", Value: "agent-1"},
		{Key: "user_id", Value: "user-1"},
		{Key: "type_id", Value: "build"},
		{Key: "display_name", Value: "agent-1"},
		{Key: "status", Value: "active"},
		{Key: "created_at", Value: now},
		{Key: "updated_at", Value: now},
	}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	return s, func() map[string]string {
		var doc struct {
			Settings map[string]string `bson:"settings"`
		}
		if err := s.DB().Collection("agents").
			FindOne(context.Background(), bson.D{{Key: "_id", Value: "agent-1"}}).Decode(&doc); err != nil {
			t.Fatalf("read agent: %v", err)
		}
		return doc.Settings
	}
}

func TestSetAgentSettings_RejectsUnknownKeysAndValues(t *testing.T) {
	s, _ := agentWithSettings(t)

	cases := map[string]map[string]string{
		"unknown key":        {"shell_timeout": "5"},
		"bad mode value":     {"scheduled_task_mode": "turbo"},
		"empty key":          {"": "build"},
		"unknown with valid": {"scheduled_task_mode": "plan", "max_iterations": "9"},
	}
	for name, settings := range cases {
		if err := s.SetAgentSettings("agent-1", settings); !errors.Is(err, store.ErrUnsupportedAgentSetting) {
			t.Errorf("%s: err = %v, want ErrUnsupportedAgentSetting", name, err)
		}
	}
}

// TestSetAgentSettings_ReplacesAndDropsEmptyValues pins the map semantics: a
// present-and-valid map replaces the whole set, and an empty value means
// "inherit" — it is dropped rather than stored as an empty string.
func TestSetAgentSettings_ReplacesAndDropsEmptyValues(t *testing.T) {
	s, read := agentWithSettings(t)

	if err := s.SetAgentSettings("agent-1", map[string]string{"scheduled_task_mode": "plan"}); err != nil {
		t.Fatalf("SetAgentSettings: %v", err)
	}
	if got := read(); got["scheduled_task_mode"] != "plan" {
		t.Fatalf("settings = %v, want scheduled_task_mode=plan", got)
	}

	// {} clears the stored map.
	if err := s.SetAgentSettings("agent-1", map[string]string{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("settings = %v, want cleared", got)
	}

	// An explicit empty value is the same as not setting it.
	if err := s.SetAgentSettings("agent-1", map[string]string{"scheduled_task_mode": ""}); err != nil {
		t.Fatalf("empty value: %v", err)
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("settings = %v, want the empty value dropped", got)
	}
}

func TestSetAgentSettings_MissingAgent(t *testing.T) {
	s := storetest.Connect(t)

	err := s.SetAgentSettings("agent-1", map[string]string{"scheduled_task_mode": "plan"})
	if !errors.Is(err, store.ErrAgentNotFound) {
		t.Fatalf("err = %v, want ErrAgentNotFound", err)
	}
}
