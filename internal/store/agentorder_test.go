package store_test

// The sidebar's order: which agents sit in which group and in what order. The
// arrange call is the only writer; a bad id must change nothing.

import (
	"errors"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

func orderAgent(t *testing.T, s *store.Store, id, userID, groupID string) {
	t.Helper()
	if err := s.CreateAgent(store.AgentRecord{
		ID: id, UserID: userID, TypeID: "build", GroupID: groupID,
		DisplayName: id, MaxConcurrentMessages: 4,
	}); err != nil {
		t.Fatalf("CreateAgent(%s): %v", id, err)
	}
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestAgentOrderArrange(t *testing.T) {
	s := groupStore(t)
	work := mustCreateGroup(t, s, "user-1", "工作")
	personal := mustCreateGroup(t, s, "user-1", "个人")

	orderAgent(t, s, "a1", "user-1", work.ID)
	orderAgent(t, s, "a2", "user-1", work.ID)
	orderAgent(t, s, "a3", "user-1", work.ID)
	orderAgent(t, s, "b1", "user-1", personal.ID)
	orderAgent(t, s, "u1", "user-1", "")

	// Before any arrange, the creation order stands: CreateAgent writes a
	// millisecond position, so each new agent sorts after the previous one.
	groups, ungrouped, err := s.ListAgentOrder("user-1")
	if err != nil {
		t.Fatalf("ListAgentOrder: %v", err)
	}
	if !sameIDs(groups[work.ID], []string{"a1", "a2", "a3"}) {
		t.Fatalf("work order = %v", groups[work.ID])
	}
	if !sameIDs(groups[personal.ID], []string{"b1"}) {
		t.Fatalf("personal order = %v", groups[personal.ID])
	}
	if !sameIDs(ungrouped, []string{"u1"}) {
		t.Fatalf("ungrouped = %v", ungrouped)
	}

	// Reverse two, move the third to the other group, and take u1 into work.
	if err := s.ArrangeAgents("user-1", map[string][]string{
		work.ID:     {"a3", "a1", "u1"},
		personal.ID: {"b1", "a2"},
	}, []string{}); err != nil {
		t.Fatalf("ArrangeAgents: %v", err)
	}

	groups, ungrouped, err = s.ListAgentOrder("user-1")
	if err != nil {
		t.Fatalf("ListAgentOrder: %v", err)
	}
	if !sameIDs(groups[work.ID], []string{"a3", "a1", "u1"}) {
		t.Fatalf("work order after arrange = %v", groups[work.ID])
	}
	if !sameIDs(groups[personal.ID], []string{"b1", "a2"}) {
		t.Fatalf("personal order after arrange = %v", groups[personal.ID])
	}
	if len(ungrouped) != 0 {
		t.Fatalf("ungrouped after arrange = %v", ungrouped)
	}

	// A partial request only touches what it names: a2 goes back to ungrouped
	// and everything else keeps its place.
	if err := s.ArrangeAgents("user-1", map[string][]string{}, []string{"a2"}); err != nil {
		t.Fatalf("ArrangeAgents(ungrouped): %v", err)
	}
	groups, ungrouped, err = s.ListAgentOrder("user-1")
	if err != nil {
		t.Fatalf("ListAgentOrder: %v", err)
	}
	if !sameIDs(groups[work.ID], []string{"a3", "a1", "u1"}) {
		t.Fatalf("work order changed by a partial request = %v", groups[work.ID])
	}
	if !sameIDs(groups[personal.ID], []string{"b1"}) {
		t.Fatalf("personal order after moving a2 out = %v", groups[personal.ID])
	}
	if !sameIDs(ungrouped, []string{"a2"}) {
		t.Fatalf("ungrouped after moving a2 out = %v", ungrouped)
	}
}

func TestArrangeAgentsRejectsForeignIDs(t *testing.T) {
	s := groupStore(t)
	work := mustCreateGroup(t, s, "user-1", "工作")
	foreign := mustCreateGroup(t, s, "user-2", "别人的")

	orderAgent(t, s, "a1", "user-1", work.ID)
	orderAgent(t, s, "b1", "user-2", foreign.ID)

	cases := []struct {
		name      string
		groups    map[string][]string
		ungrouped []string
	}{
		{"another user's agent", map[string][]string{work.ID: {"a1", "b1"}}, nil},
		{"another user's group", map[string][]string{foreign.ID: {"a1"}}, nil},
		{"unknown agent", map[string][]string{work.ID: {"nope"}}, nil},
		{"unknown group", map[string][]string{"nope": {"a1"}}, nil},
		{"repeated across groups", map[string][]string{work.ID: {"a1"}}, []string{"a1"}},
		{"repeated inside a group", map[string][]string{work.ID: {"a1", "a1"}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.ArrangeAgents("user-1", tc.groups, tc.ungrouped)
			if !errors.Is(err, store.ErrAgentOrderInvalid) {
				t.Fatalf("err = %v, want ErrAgentOrderInvalid", err)
			}
		})
	}

	// A rejected request changed nothing.
	groups, _, err := s.ListAgentOrder("user-1")
	if err != nil {
		t.Fatalf("ListAgentOrder: %v", err)
	}
	if !sameIDs(groups[work.ID], []string{"a1"}) {
		t.Fatalf("order changed by rejected requests: %v", groups[work.ID])
	}
}

func TestArrangeAgentsEmptyIsNoOp(t *testing.T) {
	s := groupStore(t)
	if err := s.ArrangeAgents("user-1", map[string][]string{}, nil); err != nil {
		t.Fatalf("ArrangeAgents(empty) = %v", err)
	}
}

func TestListAgentOrderSkipsDeletedAgents(t *testing.T) {
	s := groupStore(t)
	work := mustCreateGroup(t, s, "user-1", "工作")
	orderAgent(t, s, "a1", "user-1", work.ID)
	orderAgent(t, s, "a2", "user-1", work.ID)
	if err := s.DeleteAgent("a2"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	groups, _, err := s.ListAgentOrder("user-1")
	if err != nil {
		t.Fatalf("ListAgentOrder: %v", err)
	}
	if !sameIDs(groups[work.ID], []string{"a1"}) {
		t.Fatalf("deleted agent still listed: %v", groups[work.ID])
	}
}
