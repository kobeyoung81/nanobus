package store_test

// Agent groups are a user-private organization of agents: single level, one
// group per agent, and a delete must not take the agents with it.

import (
	"errors"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func groupStore(t *testing.T) *store.Store {
	t.Helper()
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "user-1", "pro", 1)
	storetest.SeedUser(t, s, "user-2", "pro", 2)
	return s
}

func mustCreateGroup(t *testing.T, s *store.Store, userID, name string) *store.AgentGroup {
	t.Helper()
	g, err := s.CreateAgentGroup(userID, name)
	if err != nil {
		t.Fatalf("CreateAgentGroup(%q): %v", name, err)
	}
	return g
}

func TestAgentGroupCreateAndList(t *testing.T) {
	s := groupStore(t)

	first := mustCreateGroup(t, s, "user-1", "工作")
	second := mustCreateGroup(t, s, "user-1", "个人")
	if first.Position != 0 || second.Position != 1 {
		t.Fatalf("positions = %d, %d; want 0, 1", first.Position, second.Position)
	}

	groups, err := s.ListAgentGroups("user-1")
	if err != nil {
		t.Fatalf("ListAgentGroups: %v", err)
	}
	if len(groups) != 2 || groups[0].ID != first.ID || groups[1].ID != second.ID {
		t.Fatalf("unexpected order: %+v", groups)
	}

	// Another user's list is empty: groups are private, not shared.
	other, err := s.ListAgentGroups("user-2")
	if err != nil {
		t.Fatalf("ListAgentGroups(user-2): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("user-2 sees user-1's groups: %+v", other)
	}
}

func TestAgentGroupNameTaken(t *testing.T) {
	s := groupStore(t)
	mustCreateGroup(t, s, "user-1", "工作")

	if _, err := s.CreateAgentGroup("user-1", " 工作 "); !errors.Is(err, store.ErrAgentGroupNameTaken) {
		t.Fatalf("duplicate (after trim) = %v, want ErrAgentGroupNameTaken", err)
	}
	// The unique index is per user, so the same name is fine elsewhere.
	mustCreateGroup(t, s, "user-2", "工作")

	if _, err := s.CreateAgentGroup("user-1", "   "); !errors.Is(err, store.ErrAgentGroupNameInvalid) {
		t.Fatalf("blank name = %v, want ErrAgentGroupNameInvalid", err)
	}
	long := make([]rune, store.MaxAgentGroupNameRunes+1)
	for i := range long {
		long[i] = '字'
	}
	if _, err := s.CreateAgentGroup("user-1", string(long)); !errors.Is(err, store.ErrAgentGroupNameInvalid) {
		t.Fatalf("over-long name = %v, want ErrAgentGroupNameInvalid", err)
	}
}

func TestAgentGroupRename(t *testing.T) {
	s := groupStore(t)
	group := mustCreateGroup(t, s, "user-1", "工作")
	mustCreateGroup(t, s, "user-1", "个人")

	if err := s.RenameAgentGroup(group.ID, "user-1", "项目"); err != nil {
		t.Fatalf("RenameAgentGroup: %v", err)
	}
	if err := s.RenameAgentGroup(group.ID, "user-1", "个人"); !errors.Is(err, store.ErrAgentGroupNameTaken) {
		t.Fatalf("rename onto another group = %v, want ErrAgentGroupNameTaken", err)
	}
	// Someone else's group is reported as missing, not as forbidden.
	if err := s.RenameAgentGroup(group.ID, "user-2", "偷"); !errors.Is(err, store.ErrAgentGroupNotFound) {
		t.Fatalf("cross-user rename = %v, want ErrAgentGroupNotFound", err)
	}
}

func TestAgentGroupDeleteUnassignsAgents(t *testing.T) {
	s := groupStore(t)
	group := mustCreateGroup(t, s, "user-1", "工作")
	other := mustCreateGroup(t, s, "user-1", "个人")

	for _, id := range []string{"agent-1", "agent-2"} {
		if err := s.CreateAgent(store.AgentRecord{
			ID: id, UserID: "user-1", TypeID: "build", GroupID: group.ID,
			DisplayName: id, MaxConcurrentMessages: 4,
		}); err != nil {
			t.Fatalf("CreateAgent(%s): %v", id, err)
		}
	}
	if err := s.CreateAgent(store.AgentRecord{
		ID: "agent-3", UserID: "user-1", TypeID: "build", GroupID: other.ID,
		DisplayName: "agent-3", MaxConcurrentMessages: 4,
	}); err != nil {
		t.Fatalf("CreateAgent(agent-3): %v", err)
	}

	unassigned, err := s.DeleteAgentGroup(group.ID, "user-1")
	if err != nil {
		t.Fatalf("DeleteAgentGroup: %v", err)
	}
	if unassigned != 2 {
		t.Fatalf("unassigned = %d, want 2", unassigned)
	}

	// The agents survive, ungrouped; the other group is untouched.
	for _, id := range []string{"agent-1", "agent-2"} {
		a, err := s.GetAgent(id)
		if err != nil || a == nil {
			t.Fatalf("GetAgent(%s) = %v, %v", id, a, err)
		}
		if a.GroupID != "" {
			t.Fatalf("%s still grouped under %q", id, a.GroupID)
		}
	}
	third, err := s.GetAgent("agent-3")
	if err != nil || third == nil || third.GroupID != other.ID {
		t.Fatalf("agent-3 grouping changed: %+v, %v", third, err)
	}

	// Deleting it again is a miss, and a cross-user delete changes nothing.
	if _, err := s.DeleteAgentGroup(group.ID, "user-1"); !errors.Is(err, store.ErrAgentGroupNotFound) {
		t.Fatalf("second delete = %v, want ErrAgentGroupNotFound", err)
	}
	if _, err := s.DeleteAgentGroup(other.ID, "user-2"); !errors.Is(err, store.ErrAgentGroupNotFound) {
		t.Fatalf("cross-user delete = %v, want ErrAgentGroupNotFound", err)
	}
	if _, err := s.GetAgent("agent-3"); err != nil {
		t.Fatalf("GetAgent(agent-3): %v", err)
	}
}

func TestAgentGroupReorder(t *testing.T) {
	s := groupStore(t)
	a := mustCreateGroup(t, s, "user-1", "A")
	b := mustCreateGroup(t, s, "user-1", "B")
	c := mustCreateGroup(t, s, "user-1", "C")

	if err := s.ReorderAgentGroups("user-1", []string{c.ID, a.ID, b.ID}); err != nil {
		t.Fatalf("ReorderAgentGroups: %v", err)
	}
	groups, err := s.ListAgentGroups("user-1")
	if err != nil {
		t.Fatalf("ListAgentGroups: %v", err)
	}
	got := []string{groups[0].ID, groups[1].ID, groups[2].ID}
	want := []string{c.ID, a.ID, b.ID}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
		if groups[i].Position != i {
			t.Fatalf("position[%d] = %d, want %d", i, groups[i].Position, i)
		}
	}

	// Missing, extra and repeated ids are refused as a whole.
	if err := s.ReorderAgentGroups("user-1", []string{a.ID, b.ID}); !errors.Is(err, store.ErrAgentGroupOrderInvalid) {
		t.Fatalf("missing id = %v, want ErrAgentGroupOrderInvalid", err)
	}
	if err := s.ReorderAgentGroups("user-1", []string{a.ID, b.ID, c.ID, c.ID}); !errors.Is(err, store.ErrAgentGroupOrderInvalid) {
		t.Fatalf("extra id = %v, want ErrAgentGroupOrderInvalid", err)
	}
	if err := s.ReorderAgentGroups("user-1", []string{a.ID, b.ID, "nope"}); !errors.Is(err, store.ErrAgentGroupOrderInvalid) {
		t.Fatalf("unknown id = %v, want ErrAgentGroupOrderInvalid", err)
	}
	if err := s.ReorderAgentGroups("user-1", []string{a.ID, a.ID, b.ID}); !errors.Is(err, store.ErrAgentGroupOrderInvalid) {
		t.Fatalf("repeated id = %v, want ErrAgentGroupOrderInvalid", err)
	}

	// A rejected reorder leaves the stored order alone.
	after, err := s.ListAgentGroups("user-1")
	if err != nil || len(after) != 3 || after[0].ID != c.ID {
		t.Fatalf("order changed by a rejected reorder: %+v, %v", after, err)
	}
}

func TestAgentGroupOwnership(t *testing.T) {
	s := groupStore(t)
	group := mustCreateGroup(t, s, "user-1", "工作")

	ok, err := s.AgentGroupExists("user-1", group.ID)
	if err != nil || !ok {
		t.Fatalf("AgentGroupExists(owner) = %v, %v", ok, err)
	}
	ok, err = s.AgentGroupExists("user-2", group.ID)
	if err != nil || ok {
		t.Fatalf("AgentGroupExists(other) = %v, %v", ok, err)
	}
	ok, err = s.AgentGroupExists("user-1", "")
	if err != nil || ok {
		t.Fatalf("AgentGroupExists(\"\") = %v, %v", ok, err)
	}
}

func TestSetAgentGroup(t *testing.T) {
	s := groupStore(t)
	group := mustCreateGroup(t, s, "user-1", "工作")
	if err := s.CreateAgent(store.AgentRecord{
		ID: "agent-1", UserID: "user-1", TypeID: "build",
		DisplayName: "A", MaxConcurrentMessages: 4,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	if err := s.SetAgentGroup("agent-1", group.ID); err != nil {
		t.Fatalf("SetAgentGroup: %v", err)
	}
	a, err := s.GetAgent("agent-1")
	if err != nil || a == nil || a.GroupID != group.ID {
		t.Fatalf("after set: %+v, %v", a, err)
	}
	// The projection must carry group_id, otherwise a later update would read
	// the agent as ungrouped.
	if err := s.UpdateAgent(store.AgentRecord{
		ID: "agent-1", UserID: "user-1", TypeID: "build",
		DisplayName: "A2", MaxConcurrentMessages: 4,
	}); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}
	a, err = s.GetAgent("agent-1")
	if err != nil || a == nil || a.GroupID != group.ID {
		t.Fatalf("update cleared the grouping: %+v, %v", a, err)
	}

	if err := s.SetAgentGroup("agent-1", ""); err != nil {
		t.Fatalf("SetAgentGroup(\"\"): %v", err)
	}
	a, err = s.GetAgent("agent-1")
	if err != nil || a == nil || a.GroupID != "" {
		t.Fatalf("after clear: %+v, %v", a, err)
	}
}

func TestCreateAgentWithGroup(t *testing.T) {
	s := groupStore(t)
	group := mustCreateGroup(t, s, "user-1", "工作")
	if err := s.CreateAgent(store.AgentRecord{
		ID: "agent-1", UserID: "user-1", TypeID: "build", GroupID: group.ID,
		DisplayName: "A", MaxConcurrentMessages: 4,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	agents, err := s.ListAgents("user-1")
	if err != nil || len(agents) != 1 {
		t.Fatalf("ListAgents = %+v, %v", agents, err)
	}
	if agents[0].GroupID != group.ID {
		t.Fatalf("listed group = %q, want %q", agents[0].GroupID, group.ID)
	}
}
