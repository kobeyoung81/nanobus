package store_test

// These tests run against a real MongoDB.
//
// What they assert is behaviour the SQL tests could not reach: that a tombstone
// is not resurrected by a status report, that flipping the default session agent
// leaves exactly one default, that the display-name check is case-insensitive
// (MySQL's default collation made it so, and an exact-match query would not be).

import (
	"strings"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// agentStore returns a store with a real MongoDB and a seeded user. The two
// reads that used to come from MySQL — the tier, and the binding's owner through
// `user_channels` — are served by the user document now that the users domain,
// with the channels it embeds, has moved too.
func agentStore(t *testing.T, tier string) *store.Store {
	t.Helper()
	s := storetest.Connect(t)
	// Binding 42: the id the fallback test asks for.
	storetest.SeedUser(t, s, "user-1", tier, 42)
	return s
}

func mustCreateAgent(t *testing.T, s *store.Store, id, userID string) {
	t.Helper()
	if err := s.CreateAgent(store.AgentRecord{
		ID: id, UserID: userID, TypeID: "build", DisplayName: id,
		MaxIterations: 10, MaxConcurrentMessages: 4,
	}); err != nil {
		t.Fatalf("CreateAgent(%s): %v", id, err)
	}
}

func TestCreateAgent_LinksSessionAgents(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")

	links, err := s.SessionAgentsForAgent("agent-1")
	if err != nil {
		t.Fatalf("SessionAgentsForAgent: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %d: %+v", len(links), links)
	}
	// Ordered by (position, def_id) — build was written at position 0.
	if links[0].DefID != "build" || !links[0].IsDefault {
		t.Fatalf("expected build to be the default first link, got %+v", links[0])
	}
	if links[1].DefID != "plan" || links[1].IsDefault {
		t.Fatalf("expected plan second and not default, got %+v", links[1])
	}

	// A second create must not duplicate the links: the SQL was an upsert, and
	// this is the part a naive $push would get wrong.
	mustCreateAgent(t, s, "agent-1", "user-1")
	links, err = s.SessionAgentsForAgent("agent-1")
	if err != nil {
		t.Fatalf("SessionAgentsForAgent after re-create: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("re-creating the agent duplicated its links: %+v", links)
	}
}

func TestCreateAgent_TierLimitCountsOnlyLiveAgents(t *testing.T) {
	s := agentStore(t, "free") // free allows 5
	ids := []string{"agent-1", "agent-2", "agent-3", "agent-4", "agent-5"}
	for _, id := range ids {
		mustCreateAgent(t, s, id, "user-1")
	}

	limitErr := s.CreateAgent(store.AgentRecord{ID: "agent-6", UserID: "user-1", DisplayName: "a6"})
	if limitErr == nil {
		t.Fatal("expected the free tier's limit to refuse a sixth agent")
	}
	if !strings.Contains(limitErr.Error(), "allows max 5 agents") {
		t.Fatalf("the refusal should name the new limit: %v", limitErr)
	}

	// A tombstone must not count against the limit.
	if err := s.DeleteAgent("agent-1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if err := s.CreateAgent(store.AgentRecord{ID: "agent-6", UserID: "user-1", DisplayName: "a6"}); err != nil {
		t.Fatalf("a deleted agent still counted against the limit: %v", err)
	}
}

// TestCreateAgent_PreservesTombstone pins the SQL upsert's status expression:
// `status = IF(status = 'deleted', 'deleted', 'provisioning')`. Re-creating an
// agent that was deleted must not quietly bring it back to life.
func TestCreateAgent_PreservesTombstone(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")
	if err := s.DeleteAgent("agent-1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	mustCreateAgent(t, s, "agent-1", "user-1")

	got, err := s.GetAgent("agent-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got == nil {
		t.Fatal("agent disappeared")
	}
	if got.Status != "deleted" {
		t.Fatalf("status = %q, want the tombstone to survive", got.Status)
	}
}

func TestSetDefaultSessionAgent_KeepsExactlyOneDefault(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")

	if err := s.SetDefaultSessionAgent("agent-1", "plan"); err != nil {
		t.Fatalf("SetDefaultSessionAgent: %v", err)
	}
	links, err := s.SessionAgentsForAgent("agent-1")
	if err != nil {
		t.Fatalf("SessionAgentsForAgent: %v", err)
	}
	defaults := 0
	for _, l := range links {
		if l.IsDefault {
			defaults++
			if l.DefID != "plan" {
				t.Fatalf("default is %q, want plan", l.DefID)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly one default, got %d: %+v", defaults, links)
	}

	// An unlinked def must be refused rather than half-applied.
	if err := s.SetDefaultSessionAgent("agent-1", "nope"); err == nil {
		t.Fatal("expected an error for an unlinked session agent")
	}
}

func TestAgentDisplayNameTaken_IsCaseInsensitive(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")
	if err := s.UpdateAgent(store.AgentRecord{
		ID: "agent-1", TypeID: "build", DisplayName: "Ops Bot",
		MaxIterations: 10, MaxConcurrentMessages: 4,
	}); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	// MySQL's default collation compared case-insensitively; MongoDB's default
	// does not, so an exact-match query would silently loosen the rule.
	taken, err := s.AgentDisplayNameTaken("user-1", "ops bot", "")
	if err != nil {
		t.Fatalf("AgentDisplayNameTaken: %v", err)
	}
	if !taken {
		t.Fatal("a differently-cased duplicate was accepted")
	}
	// The agent being edited must not collide with itself.
	taken, err = s.AgentDisplayNameTaken("user-1", "Ops Bot", "agent-1")
	if err != nil {
		t.Fatalf("AgentDisplayNameTaken (excluded): %v", err)
	}
	if taken {
		t.Fatal("the agent being edited counted as its own duplicate")
	}
}

func TestSetAgentStatus_And_Restore(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")
	if err := s.DeleteAgent("agent-1"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	// A runtime status report must not resurrect a tombstone.
	if err := s.SetAgentStatus("agent-1", "running"); err != nil {
		t.Fatalf("SetAgentStatus: %v", err)
	}
	got, err := s.GetAgent("agent-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.Status != "deleted" {
		t.Fatalf("status = %q, want deleted", got.Status)
	}

	if err := s.RestoreAgent("agent-1"); err != nil {
		t.Fatalf("RestoreAgent: %v", err)
	}
	got, err = s.GetAgent("agent-1")
	if err != nil {
		t.Fatalf("GetAgent after restore: %v", err)
	}
	if got.Status != "provisioning" {
		t.Fatalf("status = %q, want provisioning", got.Status)
	}
	// Restoring a live agent is a no-op that reports failure, so a caller cannot
	// mistake it for work done.
	if err := s.RestoreAgent("agent-1"); err == nil {
		t.Fatal("expected an error restoring an agent that is not deleted")
	}
}

func TestListAgentsFiltered_HidesTombstones(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")
	mustCreateAgent(t, s, "agent-2", "user-1")
	if err := s.DeleteAgent("agent-2"); err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}

	live, err := s.ListAgents("user-1")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(live) != 1 || live[0].ID != "agent-1" {
		t.Fatalf("live agents = %+v", live)
	}

	all, err := s.ListAgentsFiltered("user-1", true)
	if err != nil {
		t.Fatalf("ListAgentsFiltered: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both agents with includeDeleted, got %d", len(all))
	}
}

func TestBindingAgent_PreferenceWinsOverFallback(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")

	// No preference yet: the fallback goes through the channel embedded in the
	// user document to the owner, then finds that user's first active agent.
	got, err := s.GetBindingAgent(42)
	if err != nil {
		t.Fatalf("GetBindingAgent: %v", err)
	}
	if got != "agent-1" {
		t.Fatalf("fallback = %q, want agent-1", got)
	}

	if err := s.SetBindingAgent(42, "agent-pinned"); err != nil {
		t.Fatalf("SetBindingAgent: %v", err)
	}
	got, err = s.GetBindingAgent(42)
	if err != nil {
		t.Fatalf("GetBindingAgent after pin: %v", err)
	}
	if got != "agent-pinned" {
		t.Fatalf("preference = %q, want agent-pinned", got)
	}

	// Clearing falls back again.
	if err := s.SetBindingAgent(42, ""); err != nil {
		t.Fatalf("SetBindingAgent(clear): %v", err)
	}
	got, err = s.GetBindingAgent(42)
	if err != nil {
		t.Fatalf("GetBindingAgent after clear: %v", err)
	}
	if got != "agent-1" {
		t.Fatalf("after clearing the preference = %q, want the fallback agent-1", got)
	}
}

// TestSaveAgentSettingsRoundTrips pins the field the deprecated columns used to
// carry: settings now live in the document, so a read must return them.
func TestUpdateAgent_RoundTripsFields(t *testing.T) {
	s := agentStore(t, "pro")
	mustCreateAgent(t, s, "agent-1", "user-1")

	if err := s.UpdateAgent(store.AgentRecord{
		ID: "agent-1", TypeID: "review", DisplayName: "Reviewer",
		Description:   "reviews things",
		MaxIterations: 25, MaxConcurrentMessages: 8,
	}); err != nil {
		t.Fatalf("UpdateAgent: %v", err)
	}

	got, err := s.GetAgent("agent-1")
	if err != nil {
		t.Fatalf("GetAgent: %v", err)
	}
	if got.TypeID != "review" || got.DisplayName != "Reviewer" || got.Description != "reviews things" {
		t.Fatalf("fields did not round-trip: %+v", got)
	}
	if got.MaxIterations != 25 || got.MaxConcurrentMessages != 8 {
		t.Fatalf("fields did not round-trip: %+v", got)
	}

	// Changing the primary type links it and makes it the default, leaving the
	// previously linked modes switchable.
	links, err := s.SessionAgentsForAgent("agent-1")
	if err != nil {
		t.Fatalf("SessionAgentsForAgent: %v", err)
	}
	found := false
	for _, l := range links {
		if l.DefID == "review" {
			found = true
			if !l.IsDefault {
				t.Fatalf("the new type is not the default: %+v", links)
			}
		}
	}
	if !found {
		t.Fatalf("the new type was not linked: %+v", links)
	}
}
