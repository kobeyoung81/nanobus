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
	// The container-request indexes moved to the module that writes that
	// collection; its own manifest and tests declare and assert them.
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
