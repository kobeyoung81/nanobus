package store_test

// The users domain is the one whose *shape* the migration changes: user_channels
// became an array inside the user document, because that is what replaces the
// three-statement transaction the SQL onboarding needed. What these tests pin is
// therefore both the API's behaviour and the two properties the design leans on:
// the identity's uniqueness index (which is what makes a racing onboarding fail
// atomically instead of needing a rollback), and the binding id that survives
// re-parenting (container requests and agent names quote it).

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func readUser(t *testing.T, s *store.Store, userID string) bson.M {
	t.Helper()
	var doc bson.M
	if err := s.DB().Collection("auth_users").
		FindOne(context.Background(), bson.D{{Key: "_id", Value: userID}}).Decode(&doc); err != nil {
		t.Fatalf("read user %s: %v", userID, err)
	}
	return doc
}

// seedContainerRequest inserts a container request directly, which is how a test
// gives one a status or a creation time the API cannot set.
func seedContainerRequest(t *testing.T, s *store.Store, userID string, bindingID *int64, agentID, level, status string, createdAt time.Time) {
	t.Helper()
	doc := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "agent_id", Value: agentID},
		{Key: "level", Value: level},
		{Key: "status", Value: status},
		{Key: "created_at", Value: createdAt},
		{Key: "updated_at", Value: createdAt},
	}
	if bindingID != nil {
		doc = append(doc, bson.E{Key: "binding_id", Value: *bindingID})
	}
	if _, err := s.DB().Collection("provisioner_container_requests").InsertOne(context.Background(), doc); err != nil {
		t.Fatalf("seed container request for %s: %v", userID, err)
	}
}

func TestRegisterUser_AppliesTheColumnDefaults(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.RegisterUser("u-1", ""); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	doc := readUser(t, s, "u-1")
	if doc["status"] != "active" || doc["tier"] != "free" {
		t.Fatalf("unexpected defaults: %+v", doc)
	}
	if _, ok := doc["display_name"]; ok {
		t.Fatalf("an empty display name was stored: %+v", doc)
	}
	if doc["created_at"] == nil || doc["updated_at"] == nil {
		t.Fatalf("timestamps were not set: %+v", doc)
	}
}

func TestRegisterUser_WithDisplayName(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.RegisterUser("u-1", "Ada"); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if got := readUser(t, s, "u-1")["display_name"]; got != "Ada" {
		t.Fatalf("display_name = %v, want Ada", got)
	}
}

// TestRegisterUser_KeepsAStoredName: registering without a name must not erase
// the one the account already had (the SQL's COALESCE(VALUES(...), ...)).
func TestRegisterUser_KeepsAStoredName(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.RegisterUser("u-1", "Ada"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := s.RegisterUser("u-1", ""); err != nil {
		t.Fatalf("second register: %v", err)
	}
	if got := readUser(t, s, "u-1")["display_name"]; got != "Ada" {
		t.Fatalf("the stored name was overwritten: %v", got)
	}
}

func TestLinkChannel_AddsAFreshBinding(t *testing.T) {
	s := storetest.Connect(t)
	if err := s.RegisterUser("u-1", ""); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}

	if err := s.LinkChannel("u-1", "telegram", "sender-1"); err != nil {
		t.Fatalf("LinkChannel: %v", err)
	}

	resolved, err := s.ResolveUserBinding("telegram", "sender-1")
	if err != nil {
		t.Fatalf("ResolveUserBinding: %v", err)
	}
	if resolved.BindingID == 0 || resolved.UserID != "u-1" {
		t.Fatalf("unexpected binding: %+v", resolved)
	}

	// Linking again is a no-op, not a second channel.
	if err := s.LinkChannel("u-1", "telegram", "sender-1"); err != nil {
		t.Fatalf("second LinkChannel: %v", err)
	}
	doc := readUser(t, s, "u-1")
	if channels, _ := doc["channels"].(bson.A); len(channels) != 1 {
		t.Fatalf("expected one channel, got %+v", doc["channels"])
	}
}

// TestLinkChannel_ReparentsAndKeepsTheBinding is the reason the channel is
// embedded with its binding id rather than re-created: the SQL's upsert moved the
// row and kept its AUTO_INCREMENT id, and container requests and "binding-N"
// agent names quote that id.
func TestLinkChannel_ReparentsAndKeepsTheBinding(t *testing.T) {
	s := storetest.Connect(t)
	if err := s.RegisterUser("u-1", ""); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if err := s.RegisterUser("u-2", ""); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if err := s.LinkChannel("u-1", "telegram", "sender-1"); err != nil {
		t.Fatalf("LinkChannel: %v", err)
	}
	before, err := s.ResolveUserBinding("telegram", "sender-1")
	if err != nil {
		t.Fatalf("ResolveUserBinding: %v", err)
	}
	// A disabled identity must stay disabled across the move: the SQL kept the
	// whole row, not just its identity.
	if err := s.DisableChannel("u-1", "telegram"); err != nil {
		t.Fatalf("DisableChannel: %v", err)
	}

	if err := s.LinkChannel("u-2", "telegram", "sender-1"); err != nil {
		t.Fatalf("re-link: %v", err)
	}

	// The new owner holds the same element: same binding id, and still disabled.
	channels, _ := readUser(t, s, "u-2")["channels"].(bson.A)
	if len(channels) != 1 {
		t.Fatalf("the identity did not arrive at u-2: %+v", channels)
	}
	element, _ := channels[0].(bson.D)
	moved := map[string]any{}
	for _, e := range element {
		moved[e.Key] = e.Value
	}
	if moved["binding_id"] != before.BindingID {
		t.Fatalf("the binding id changed on re-parenting: %v → %v", before.BindingID, moved["binding_id"])
	}
	if moved["enabled"] != false {
		t.Fatalf("the whole element should have moved, enabled included: %+v", moved)
	}
	// Resolving it says the same thing.
	if _, err := s.ResolveUserBinding("telegram", "sender-1"); !errors.Is(err, store.ErrChannelDisabled) {
		t.Fatalf("expected ErrChannelDisabled, got %v", err)
	}
	// And it is gone from the previous owner.
	if channels, _ := readUser(t, s, "u-1")["channels"].(bson.A); len(channels) != 0 {
		t.Fatalf("the identity is still linked to u-1: %+v", channels)
	}
}

func TestLinkChannel_UnknownUser(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.LinkChannel("ghost", "telegram", "sender-1"); err == nil {
		t.Fatal("linking a channel to a user that does not exist was accepted")
	}
}

func TestResolveUser_ExistingUser(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)

	userID, isNew, err := s.ResolveUser("test", "sender-u-1")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if userID != "u-1" || isNew {
		t.Fatalf("userID = %q isNew = %v", userID, isNew)
	}
}

func TestResolveUser_DisabledChannel(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)
	if err := s.DisableChannel("u-1", "test"); err != nil {
		t.Fatalf("DisableChannel: %v", err)
	}

	_, _, err := s.ResolveUser("test", "sender-u-1")
	if !errors.Is(err, store.ErrChannelDisabled) {
		t.Fatalf("expected ErrChannelDisabled, got %v", err)
	}
}

// TestResolveUser_AutoOnboard: a new identity gets a user, a binding and the
// container request that provisions its runtime — the three writes the SQL did in
// one transaction.
func TestResolveUser_AutoOnboard(t *testing.T) {
	s := storetest.Connect(t)

	userID, isNew, err := s.ResolveUser("telegram", "sender-1")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if !isNew || userID == "" {
		t.Fatalf("expected a new user, got %q isNew=%v", userID, isNew)
	}

	status, tier, err := s.GetUserInfo(userID)
	if err != nil {
		t.Fatalf("GetUserInfo: %v", err)
	}
	if status != "pending" || tier != "free" {
		t.Fatalf("status = %q tier = %q, want pending/free", status, tier)
	}

	resolved, err := s.ResolveUserBinding("telegram", "sender-1")
	if err != nil {
		t.Fatalf("ResolveUserBinding: %v", err)
	}
	bindingID := resolved.BindingID
	if bindingID == 0 {
		t.Fatalf("no binding was allocated: %+v", resolved)
	}

	requests, err := s.ListContainers(userID, 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("expected one container request, got %+v", requests)
	}
	if requests[0].Status != "pending" || requests[0].Level != "free" {
		t.Fatalf("unexpected container request: %+v", requests[0])
	}
	if requests[0].AgentID != fmt.Sprintf("binding-%d", bindingID) {
		t.Fatalf("unexpected agent id: %q", requests[0].AgentID)
	}
}

// TestResolveUserBinding_ConcurrentOnboarding is the O-2 test: the SQL needed a
// transaction (insert the user, insert the channel, roll back on the duplicate
// key) to survive two callers racing for one identity. Here the identity's
// uniqueness index fails the whole user document, so the loser's read finds the
// winner's row — and no orphan user is left behind.
func TestResolveUserBinding_ConcurrentOnboarding(t *testing.T) {
	s := storetest.Connect(t)

	const racers = 8
	var wg sync.WaitGroup
	results := make([]store.ResolvedBinding, racers)
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = s.ResolveUserBinding("telegram", "sender-1")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	first := results[0]
	for i, got := range results {
		if got.UserID != first.UserID || got.BindingID != first.BindingID {
			t.Fatalf("racer %d resolved a different user: %+v vs %+v", i, got, first)
		}
	}

	// Exactly one user, one binding, one container request.
	n, err := s.DB().Collection("auth_users").CountDocuments(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected one user, got %d — a loser left an orphan behind", n)
	}
	if requests, err := s.ListContainers(first.UserID, 10); err != nil {
		t.Fatalf("ListContainers: %v", err)
	} else if len(requests) != 1 {
		t.Fatalf("expected one container request, got %+v", requests)
	}
}

// TestUsers_IdentityIsUnique pins the manifest index the concurrency test above
// depends on: the (channel, sender_id) pair that was the SQL primary key.
func TestUsers_IdentityIsUnique(t *testing.T) {
	s := storetest.Connect(t)
	ctx := context.Background()

	insert := func(userID string) error {
		_, err := s.DB().Collection("auth_users").InsertOne(ctx, bson.D{
			{Key: "_id", Value: userID},
			{Key: "status", Value: "active"},
			{Key: "tier", Value: "free"},
			{Key: "channels", Value: bson.A{bson.D{
				{Key: "binding_id", Value: 1},
				{Key: "channel", Value: "telegram"},
				{Key: "sender_id", Value: "sender-1"},
				{Key: "enabled", Value: true},
			}}},
		})
		return err
	}
	if err := insert("u-1"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert("u-2"); err == nil {
		t.Fatal("an identity was linked to two users: the unique index is missing")
	}
}

func TestListUsers_Sorted(t *testing.T) {
	s := storetest.Connect(t)
	for _, id := range []string{"u-2", "u-1", "u-3"} {
		if err := s.RegisterUser(id, ""); err != nil {
			t.Fatalf("RegisterUser: %v", err)
		}
	}

	users, err := s.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 3 || users[0] != "u-1" || users[2] != "u-3" {
		t.Fatalf("unexpected order: %v", users)
	}
}

func TestUpdateUserStatus(t *testing.T) {
	s := storetest.Connect(t)
	if err := s.RegisterUser("u-1", ""); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}

	if err := s.UpdateUserStatus("u-1", "suspended"); err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if status, _, err := s.GetUserInfo("u-1"); err != nil || status != "suspended" {
		t.Fatalf("status = %q (err %v)", status, err)
	}
	// Idempotent: setting the status it already holds is not an error.
	if err := s.UpdateUserStatus("u-1", "suspended"); err != nil {
		t.Fatalf("idempotent UpdateUserStatus: %v", err)
	}
}

func TestUpdateUserStatus_NotFound(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.UpdateUserStatus("ghost", "active"); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestGetUserInfo_NotFound(t *testing.T) {
	s := storetest.Connect(t)

	if _, _, err := s.GetUserInfo("ghost"); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestUserIDForBinding(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)

	userID, err := s.UserIDForBinding(7)
	if err != nil {
		t.Fatalf("UserIDForBinding: %v", err)
	}
	if userID != "u-1" {
		t.Fatalf("owner = %q, want u-1", userID)
	}
	// An unowned binding is not an error, it is nobody's.
	if owner, err := s.UserIDForBinding(99); err != nil || owner != "" {
		t.Fatalf("expected no owner, got %q (err %v)", owner, err)
	}
}

func TestEnableDisableChannel(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)

	if err := s.DisableChannel("u-1", "test"); err != nil {
		t.Fatalf("DisableChannel: %v", err)
	}
	if _, err := s.ResolveUserBinding("test", "sender-u-1"); !errors.Is(err, store.ErrChannelDisabled) {
		t.Fatalf("channel not disabled: %v", err)
	}
	if err := s.EnableChannel("u-1", "test"); err != nil {
		t.Fatalf("EnableChannel: %v", err)
	}
	if _, err := s.ResolveUserBinding("test", "sender-u-1"); err != nil {
		t.Fatalf("channel not re-enabled: %v", err)
	}
}

func TestCreateContainerRequest_UnboundAndBindingScoped(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)

	// The user-level request carries no binding, so it cannot collide with
	// another user's through the unique index.
	if err := s.CreateContainerRequest("u-1", 0, "agent-1", "pro"); err != nil {
		t.Fatalf("CreateContainerRequest: %v", err)
	}
	requests, err := s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(requests) != 1 || requests[0].BindingID != 0 {
		t.Fatalf("unexpected request: %+v", requests)
	}

	// A binding-scoped request is a different slot.
	if err := s.CreateContainerRequest("u-1", 7, "agent-2", "pro"); err != nil {
		t.Fatalf("CreateContainerRequest: %v", err)
	}
	requests, err = s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected two requests, got %+v", requests)
	}
}

func TestCreateContainerRequest_AlreadyRequested(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)

	if err := s.CreateContainerRequest("u-1", 0, "agent-1", "pro"); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := s.CreateContainerRequest("u-1", 0, "agent-1", "pro"); !errors.Is(err, store.ErrContainerAlreadyRequested) {
		t.Fatalf("expected ErrContainerAlreadyRequested, got %v", err)
	}
}

// TestCreateContainerRequest_RevivesDisposed: a disposed request still holds the
// slot, so it is revived rather than duplicated.
func TestCreateContainerRequest_RevivesDisposed(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)
	seedContainerRequest(t, s, "u-1", nil, "agent-1", "free", "disposed", time.Now().UTC())

	if err := s.CreateContainerRequest("u-1", 0, "agent-2", "ultra"); err != nil {
		t.Fatalf("CreateContainerRequest: %v", err)
	}

	requests, err := s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("a second request was inserted instead of reviving: %+v", requests)
	}
	if requests[0].Status != "pending" || requests[0].Level != "ultra" || requests[0].AgentID != "agent-2" {
		t.Fatalf("the disposed request was not revived in place: %+v", requests[0])
	}
}

func TestEnsureContainerRequest_UsesTheTier(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "ultra", 7)

	if err := s.EnsureContainerRequest("u-1", "agent-1"); err != nil {
		t.Fatalf("EnsureContainerRequest: %v", err)
	}
	// Idempotent: a request already in place is not an error.
	if err := s.EnsureContainerRequest("u-1", "agent-1"); err != nil {
		t.Fatalf("second EnsureContainerRequest: %v", err)
	}

	requests, err := s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(requests) != 1 || requests[0].Level != "ultra" {
		t.Fatalf("unexpected requests: %+v", requests)
	}
	if err := s.EnsureContainerRequest("ghost", "agent-1"); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestListContainers_NewestFirst(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)

	base := time.Now().UTC()
	seedContainerRequest(t, s, "u-1", nil, "a", "free", "disposed", base.Add(-2*time.Hour))
	seedContainerRequest(t, s, "u-1", nil, "b", "free", "running", base.Add(-time.Hour))
	seedContainerRequest(t, s, "u-1", nil, "c", "free", "pending", base)

	requests, err := s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(requests))
	}
	if requests[0].AgentID != "c" || requests[2].AgentID != "a" {
		t.Fatalf("expected newest first, got %+v", requests)
	}

	limited, err := s.ListContainers("u-1", 1)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if len(limited) != 1 || limited[0].AgentID != "c" {
		t.Fatalf("limit not honoured: %+v", limited)
	}
}

func TestDisposeContainerRequest(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)
	seedContainerRequest(t, s, "u-1", nil, "a", "free", "running", time.Now().UTC())

	if err := s.DisposeContainerRequest("u-1", 0); err != nil {
		t.Fatalf("DisposeContainerRequest: %v", err)
	}
	requests, err := s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if requests[0].Status != "disposing" {
		t.Fatalf("status = %q, want disposing", requests[0].Status)
	}

	// Nothing left to dispose.
	if err := s.DisposeContainerRequest("u-1", 0); !errors.Is(err, store.ErrContainerNotFound) {
		t.Fatalf("expected ErrContainerNotFound, got %v", err)
	}
}

func TestDisposeContainerRequest_ByBinding(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)
	binding := int64(7)
	seedContainerRequest(t, s, "u-1", &binding, "a", "free", "running", time.Now().UTC())
	// Another user's request must not be touched.
	storetest.SeedUser(t, s, "u-2", "pro", 8)
	other := int64(8)
	seedContainerRequest(t, s, "u-2", &other, "b", "free", "running", time.Now().UTC())

	if err := s.DisposeContainerRequest("u-1", 7); err != nil {
		t.Fatalf("DisposeContainerRequest: %v", err)
	}
	if requests, err := s.ListContainers("u-2", 10); err != nil || requests[0].Status != "running" {
		t.Fatalf("another user's request was disposed: %+v (err %v)", requests, err)
	}
}

// TestRestartContainerRequest_FollowsTheTier: the SQL joined users so the rebuilt
// container is sized from the user's *current* tier, not the level recorded at
// the last provision.
func TestRestartContainerRequest_FollowsTheTier(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "ultra", 7)
	seedContainerRequest(t, s, "u-1", nil, "a", "free", "running", time.Now().UTC())

	if err := s.RestartContainerRequest("u-1"); err != nil {
		t.Fatalf("RestartContainerRequest: %v", err)
	}
	requests, err := s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if requests[0].Status != "restarting" || requests[0].Level != "ultra" {
		t.Fatalf("unexpected request: %+v", requests[0])
	}
}

func TestRestartContainerRequest_NotFound(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)
	// Disposed, so there is nothing running to restart.
	seedContainerRequest(t, s, "u-1", nil, "a", "free", "disposed", time.Now().UTC())

	if err := s.RestartContainerRequest("u-1"); !errors.Is(err, store.ErrContainerNotFound) {
		t.Fatalf("expected ErrContainerNotFound, got %v", err)
	}
	// A missing user matched no row in the SQL either.
	if err := s.RestartContainerRequest("ghost"); !errors.Is(err, store.ErrContainerNotFound) {
		t.Fatalf("expected ErrContainerNotFound for an unknown user, got %v", err)
	}
}

func TestReprovisionContainerRequest(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "ultra", 7)
	seedContainerRequest(t, s, "u-1", nil, "a", "free", "disposed", time.Now().UTC())

	if err := s.ReprovisionContainerRequest("u-1"); err != nil {
		t.Fatalf("ReprovisionContainerRequest: %v", err)
	}
	requests, err := s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	if requests[0].Status != "pending" || requests[0].Level != "ultra" {
		t.Fatalf("unexpected request: %+v", requests[0])
	}

	// Nothing left to revive.
	if err := s.ReprovisionContainerRequest("u-1"); !errors.Is(err, store.ErrContainerNotFound) {
		t.Fatalf("expected ErrContainerNotFound, got %v", err)
	}
}

func TestUpdateContainerStatus(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)
	seedContainerRequest(t, s, "u-1", nil, "a", "free", "creating", time.Now().UTC())

	if err := s.UpdateContainerStatus("u-1", 0, "running"); err != nil {
		t.Fatalf("UpdateContainerStatus: %v", err)
	}
	if requests, err := s.ListContainers("u-1", 10); err != nil || requests[0].Status != "running" {
		t.Fatalf("status not updated: %+v (err %v)", requests, err)
	}

	// Setting the status it already holds is not "not found".
	if err := s.UpdateContainerStatus("u-1", 0, "running"); err != nil {
		t.Fatalf("idempotent UpdateContainerStatus: %v", err)
	}

	if err := s.UpdateContainerStatus("ghost", 0, "running"); err == nil {
		t.Fatal("expected an error for an unknown user")
	}
}

// TestUpdateContainerStatus_DisposeWins: once a container is being disposed, a
// racing provision completion must not override it.
func TestUpdateContainerStatus_DisposeWins(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)
	seedContainerRequest(t, s, "u-1", nil, "a", "free", "disposing", time.Now().UTC())

	if err := s.UpdateContainerStatus("u-1", 0, "running"); err == nil {
		t.Fatal("a disposing request was moved back to running")
	}
	// Only 'disposed' gets through.
	if err := s.UpdateContainerStatus("u-1", 0, "disposed"); err != nil {
		t.Fatalf("UpdateContainerStatus(disposed): %v", err)
	}
	if requests, err := s.ListContainers("u-1", 10); err != nil || requests[0].Status != "disposed" {
		t.Fatalf("status = %+v (err %v)", requests, err)
	}
}

// TestPollPendingContainers_ClaimsAndNames: the runner's poll returns the oldest
// ten, claims them, and carries the account name the container runs as.
func TestPollPendingContainers_ClaimsAndNames(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)
	// SeedUser leaves no display name, so set one: the runner names the container
	// Linux user after it.
	if _, err := s.DB().Collection("auth_users").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: "u-1"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "display_name", Value: "Ada"}}}},
	); err != nil {
		t.Fatalf("set display name: %v", err)
	}

	base := time.Now().UTC()
	seedContainerRequest(t, s, "u-1", nil, "first", "free", "pending", base.Add(-time.Hour))
	seedContainerRequest(t, s, "u-1", nil, "second", "free", "disposing", base)
	seedContainerRequest(t, s, "u-1", nil, "third", "free", "running", base)

	requests, err := s.PollPendingContainers("runner-1")
	if err != nil {
		t.Fatalf("PollPendingContainers: %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("expected the pending and disposing requests, got %+v", requests)
	}
	if requests[0].AgentID != "first" || requests[1].AgentID != "second" {
		t.Fatalf("expected oldest first, got %+v", requests)
	}
	if requests[0].DisplayName != "Ada" {
		t.Fatalf("display name missing: %+v", requests[0])
	}

	// The claims are written: pending → creating, disposing keeps its status but
	// gains the runner.
	all, err := s.ListContainers("u-1", 10)
	if err != nil {
		t.Fatalf("ListContainers: %v", err)
	}
	byAgent := map[string]nanoprotocol.ContainerRequest{}
	for _, r := range all {
		byAgent[r.AgentID] = r
	}
	if byAgent["first"].Status != "creating" {
		t.Fatalf("the pending request was not claimed: %+v", byAgent["first"])
	}
	if byAgent["second"].Status != "disposing" {
		t.Fatalf("the disposing request changed status: %+v", byAgent["second"])
	}
	if byAgent["third"].Status != "running" {
		t.Fatalf("a running request was claimed: %+v", byAgent["third"])
	}

	// Nothing left to claim.
	if again, err := s.PollPendingContainers("runner-1"); err != nil {
		t.Fatalf("second poll: %v", err)
	} else if len(again) == 0 {
		t.Fatalf("expected the disposing request to still be claimable: %+v", again)
	}
}

// TestPollPendingContainers_ConcurrentPollers: the SQL locked the rows with FOR
// UPDATE SKIP LOCKED, so two runners could not both be handed the same request.
// The conditional claim is what holds that here.
func TestPollPendingContainers_ConcurrentPollers(t *testing.T) {
	s := storetest.Connect(t)
	storetest.SeedUser(t, s, "u-1", "pro", 7)

	base := time.Now().UTC()
	const count = 20
	for i := 0; i < count; i++ {
		seedContainerRequest(t, s, "u-1", nil, fmt.Sprintf("a-%d", i), "free", "pending", base.Add(time.Duration(i)*time.Millisecond))
	}

	const pollers = 4
	var mu sync.Mutex
	claimed := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < pollers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				requests, err := s.PollPendingContainers("runner")
				if err != nil {
					t.Errorf("PollPendingContainers: %v", err)
					return
				}
				if len(requests) == 0 {
					return
				}
				mu.Lock()
				for _, r := range requests {
					claimed[r.AgentID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	for agentID, n := range claimed {
		if n > 1 {
			t.Fatalf("request %q was claimed %d times", agentID, n)
		}
	}
}

func TestMigrations_DeclareUserIndexes(t *testing.T) {
	s := storetest.Connect(t)

	// The identity index is the SQL primary key of user_channels, and it is what
	// makes a racing onboarding fail atomically.
	assertIndexKeys(t, s.DB().Collection("auth_users"), "channel_identity_unique", [][2]any{
		{"channels.channel", int32(1)}, {"channels.sender_id", int32(1)},
	})
	// The binding id was UNIQUE on its own table; container requests quote it.
	assertIndexKeys(t, s.DB().Collection("auth_users"), "binding_id_unique", [][2]any{
		{"channels.binding_id", int32(1)},
	})
	// Uniqueness itself, not just the key order: these two *are* the constraints.
	assertIndexUnique(t, s.DB().Collection("auth_users"), "channel_identity_unique")
	assertIndexUnique(t, s.DB().Collection("auth_users"), "binding_id_unique")
	// A unique index that skips documents without a binding is the MongoDB form
	// of MySQL's UNIQUE-permitting-many-NULLs (migration 055).
	assertIndexKeys(t, s.DB().Collection("provisioner_container_requests"), "binding_unique", [][2]any{
		{"binding_id", int32(1)},
	})
	assertIndexUnique(t, s.DB().Collection("provisioner_container_requests"), "binding_unique")
	assertIndexKeys(t, s.DB().Collection("provisioner_container_requests"), "status_created", [][2]any{
		{"status", int32(1)}, {"created_at", int32(1)},
	})
	assertIndexKeys(t, s.DB().Collection("provisioner_container_requests"), "user_binding", [][2]any{
		{"user_id", int32(1)}, {"binding_id", int32(1)},
	})
}

// The identity and administration fields the portal used to own in its own SQL
// table. They live in the user document now, so the two services cannot disagree
// about one account — and the tests below pin the two things that have teeth: the
// password stamping, and the admin count that guards the last administrator.

func TestSetUserRoleTierAndPassword(t *testing.T) {
	s := storetest.Connect(t)
	if err := s.RegisterUser("u-1", "Ada"); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}

	if err := s.SetUserRole("u-1", "admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	if err := s.SetUserTier("u-1", "ultra"); err != nil {
		t.Fatalf("SetUserTier: %v", err)
	}
	// A user with no hash reads as having none, rather than as an error.
	if hash, err := s.UserPasswordHash("u-1"); err != nil || hash != "" {
		t.Fatalf("expected no hash, got %q (err %v)", hash, err)
	}

	if err := s.SetUserPassword("u-1", "bcrypt-hash", true); err != nil {
		t.Fatalf("SetUserPassword: %v", err)
	}
	hash, err := s.UserPasswordHash("u-1")
	if err != nil {
		t.Fatalf("UserPasswordHash: %v", err)
	}
	if hash != "bcrypt-hash" {
		t.Fatalf("hash = %q", hash)
	}

	doc := readUser(t, s, "u-1")
	if doc["role"] != "admin" || doc["tier"] != "ultra" || doc["must_change_password"] != true {
		t.Fatalf("fields did not land: %+v", doc)
	}
	if doc["password_changed_at"] == nil {
		t.Fatalf("password_changed_at was not stamped: %+v", doc)
	}

	// Unknown users are reported, not silently accepted.
	if err := s.SetUserRole("ghost", "admin"); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
	if err := s.SetUserPassword("ghost", "x", false); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
	if hash, err := s.UserPasswordHash("ghost"); err != nil || hash != "" {
		t.Fatalf("expected no hash for an unknown user, got %q (err %v)", hash, err)
	}
}

func TestUserExists(t *testing.T) {
	s := storetest.Connect(t)

	if exists, err := s.UserExists("ghost"); err != nil || exists {
		t.Fatalf("expected no such user, got %v (err %v)", exists, err)
	}
	if err := s.RegisterUser("u-1", ""); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if exists, err := s.UserExists("u-1"); err != nil || !exists {
		t.Fatalf("expected the user to exist, got %v (err %v)", exists, err)
	}
	// A deleted account still exists: status is a lifecycle, not a deletion.
	if err := s.UpdateUserStatus("u-1", "deleted"); err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if exists, err := s.UserExists("u-1"); err != nil || !exists {
		t.Fatalf("a deleted user must still exist: %v (err %v)", exists, err)
	}
}

// TestCountActiveAdmins is the guard the portal consults before demoting or
// deleting an admin: if it counted users without a role, or counted deleted ones,
// the last administrator could be locked out.
func TestCountActiveAdmins(t *testing.T) {
	s := storetest.Connect(t)

	count, err := s.CountActiveAdmins()
	if err != nil {
		t.Fatalf("CountActiveAdmins: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected no admins, got %d", count)
	}

	for _, u := range []string{"a-1", "a-2", "u-1"} {
		if err := s.RegisterUser(u, ""); err != nil {
			t.Fatalf("RegisterUser: %v", err)
		}
	}
	for _, u := range []string{"a-1", "a-2"} {
		if err := s.SetUserRole(u, "admin"); err != nil {
			t.Fatalf("SetUserRole: %v", err)
		}
	}
	if count, err = s.CountActiveAdmins(); err != nil || count != 2 {
		t.Fatalf("expected 2 admins, got %d (err %v)", count, err)
	}

	// A demoted admin stops counting, and so does a deleted one.
	if err := s.SetUserRole("a-1", "user"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}
	if err := s.UpdateUserStatus("a-2", "deleted"); err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if count, err = s.CountActiveAdmins(); err != nil || count != 0 {
		t.Fatalf("expected no active admins, got %d (err %v)", count, err)
	}
}

func TestListUserSummaries(t *testing.T) {
	s := storetest.Connect(t)
	base := time.Now().UTC()
	// Two users with distinct update times so the order is decided, and one
	// deleted to be filtered out by default.
	if _, err := s.DB().Collection("auth_users").InsertMany(context.Background(), []any{
		bson.D{{Key: "_id", Value: "u-1"}, {Key: "status", Value: "active"}, {Key: "tier", Value: "pro"},
			{Key: "created_at", Value: base}, {Key: "updated_at", Value: base.Add(-time.Hour)}},
		bson.D{{Key: "_id", Value: "u-2"}, {Key: "status", Value: "deleted"}, {Key: "tier", Value: "free"},
			{Key: "display_name", Value: "Gone"}, {Key: "role", Value: "admin"},
			{Key: "created_at", Value: base}, {Key: "updated_at", Value: base}},
	}); err != nil {
		t.Fatalf("seed users: %v", err)
	}

	summaries, err := s.ListUserSummaries(false, 0)
	if err != nil {
		t.Fatalf("ListUserSummaries: %v", err)
	}
	if len(summaries) != 1 || summaries[0].ID != "u-1" {
		t.Fatalf("expected only the live user, got %+v", summaries)
	}
	// An unset role reads as "user", which is what the console shows.
	if summaries[0].Role != "user" || summaries[0].Tier != "pro" {
		t.Fatalf("unexpected summary: %+v", summaries[0])
	}

	all, err := s.ListUserSummaries(true, 0)
	if err != nil {
		t.Fatalf("ListUserSummaries(includeDeleted): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both users, got %+v", all)
	}
	// Newest first, by the id as a tiebreak.
	if all[0].ID != "u-2" || all[1].ID != "u-1" {
		t.Fatalf("unexpected order: %+v", all)
	}
}

// TestUsers_EmailIsUniqueWhenPresent pins the index the identity story needs: two
// accounts cannot share an email, while accounts without one are not in the way.
func TestUsers_EmailIsUniqueWhenPresent(t *testing.T) {
	s := storetest.Connect(t)
	ctx := context.Background()
	insert := func(id, email string) error {
		doc := bson.D{{Key: "_id", Value: id}, {Key: "status", Value: "active"}, {Key: "tier", Value: "free"}}
		if email != "" {
			doc = append(doc, bson.E{Key: "email", Value: email})
		}
		_, err := s.DB().Collection("auth_users").InsertOne(ctx, doc)
		return err
	}

	if err := insert("u-1", "ada@example.com"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert("u-2", "ada@example.com"); err == nil {
		t.Fatal("a duplicate email was accepted: the unique index is missing")
	}
	// Two users with no email at all are fine — that is why the index is sparse.
	if err := insert("u-3", ""); err != nil {
		t.Fatalf("user without an email: %v", err)
	}
	if err := insert("u-4", ""); err != nil {
		t.Fatalf("second user without an email: %v", err)
	}
}

func TestMigrations_DeclareUserEmailIndex(t *testing.T) {
	s := storetest.Connect(t)

	assertIndexKeys(t, s.DB().Collection("auth_users"), "email_unique", [][2]any{{"email", int32(1)}})
	assertIndexUnique(t, s.DB().Collection("auth_users"), "email_unique")
}
