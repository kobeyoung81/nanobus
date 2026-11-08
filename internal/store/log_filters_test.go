package store

// The log pages ask one condition at a time and the store ANDs them. What a
// driver mock could not show — and what regresses silently — is that each
// condition stays in its own column and that a name is turned into the ids it
// names. These are the pure filter builders, so no MongoDB is needed.

import (
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func filterJSON(t *testing.T, filter bson.D) string {
	t.Helper()
	if len(filter) == 0 {
		return "{}"
	}
	out, err := bson.MarshalExtJSON(filter, false, false)
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}
	return string(out)
}

func TestConversationFilter_NoConditionsIsAnEmptyFilter(t *testing.T) {
	if got := conversationFilter(ConversationFilter{}, nil, nil); len(got) != 0 {
		t.Fatalf("filter = %s, want empty", filterJSON(t, got))
	}
}

func TestConversationFilter_EachConditionIsItsOwnColumn(t *testing.T) {
	got := filterJSON(t, conversationFilter(ConversationFilter{
		Session: "web:chat",
		Agent:   "ada",
		Account: "bob",
		Channel: "web",
		Status:  "active",
	}, []string{"agent-1"}, []string{"user-1"}))

	for _, want := range []string{
		// The session id condition never leaves the _id column.
		`"_id":{"$regex":"web:chat"`,
		// A name fragment adds the ids it named next to the id fragment itself.
		`{"agent_id":{"$regex":"ada","$options":"i"}}`,
		`{"agent_id":{"$in":["agent-1"]}}`,
		`{"account_id":{"$regex":"bob","$options":"i"}}`,
		`{"account_id":{"$in":["user-1"]}}`,
		// Enumerated conditions are exact matches, not patterns.
		`{"channel":"web"}`,
		`{"status":"active"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("filter is missing %s:\n%s", want, got)
		}
	}
	// Five conditions, five clauses.
	if n := strings.Count(got, `{"$and":[`); n != 1 {
		t.Fatalf("conditions are not ANDed once: %s", got)
	}
	if n := strings.Count(got, `{"$regex"`); n != 3 {
		t.Fatalf("expected three text conditions, got %d: %s", n, got)
	}
}

func TestConversationFilter_NameThatMatchesNothingOnlyKeepsTheIDFragment(t *testing.T) {
	got := filterJSON(t, conversationFilter(ConversationFilter{Agent: "ghost"}, nil, nil))
	if strings.Contains(got, "$in") {
		t.Fatalf("an unmatched name must not widen the filter: %s", got)
	}
	if !strings.Contains(got, `{"agent_id":{"$regex":"ghost","$options":"i"}}`) {
		t.Fatalf("the id fragment is missing: %s", got)
	}
}

func TestConversationFilter_TextIsQuoted(t *testing.T) {
	got := filterJSON(t, conversationFilter(ConversationFilter{Account: "a.b"}, nil, nil))
	if !strings.Contains(got, `a\\.b`) {
		t.Fatalf("the fragment was not quoted as a literal: %s", got)
	}
}

func TestAuditFilter_NoConditionsIsAnEmptyFilter(t *testing.T) {
	if got := auditFilter(AuditFilter{}, nil, nil, nil, nil); len(got) != 0 {
		t.Fatalf("filter = %s, want empty", filterJSON(t, got))
	}
}

func TestAuditFilter_ResourceNameBranchesAreScopedByType(t *testing.T) {
	got := filterJSON(t, auditFilter(AuditFilter{Resource: "ada"},
		nil, []string{"agent-1"}, []string{"user-1"}, []string{"group-1"}))

	for _, want := range []string{
		`{"resource_id":{"$regex":"ada","$options":"i"}}`,
		`{"$and":[{"resource_type":"agent"},{"resource_id":{"$in":["agent-1"]}}]}`,
		`{"$and":[{"resource_type":"user"},{"resource_id":{"$in":["user-1"]}}]}`,
		`{"$and":[{"resource_type":"agent_group"},{"resource_id":{"$in":["group-1"]}}]}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("filter is missing %s:\n%s", want, got)
		}
	}
}

func TestAuditFilter_ActorAndEnumeratedConditions(t *testing.T) {
	got := filterJSON(t, auditFilter(AuditFilter{
		Action:       "user.update",
		Actor:        "ada",
		ResourceType: "agent",
	}, []string{"user-1"}, nil, nil, nil))

	for _, want := range []string{
		`{"action":"user.update"}`,
		`{"actor_id":{"$regex":"ada","$options":"i"}}`,
		`{"actor_id":{"$in":["user-1"]}}`,
		`{"resource_type":"agent"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("filter is missing %s:\n%s", want, got)
		}
	}
}
