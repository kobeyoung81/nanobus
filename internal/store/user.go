package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanoprotocol"
	"github.com/google/uuid"
)

// ErrUserNotFound is returned when a user cannot be resolved.
var ErrUserNotFound = errors.New("user not found")

// ErrContainerAlreadyRequested is returned when a container is already pending for the user.
var ErrContainerAlreadyRequested = errors.New("container already requested")

// ErrContainerNotFound is returned when no active container request exists to dispose.
var ErrContainerNotFound = errors.New("container request not found")

// ErrChannelDisabled is returned when a user's channel is disabled.
var ErrChannelDisabled = errors.New("channel disabled")

// ResolvedBinding describes the resolved binding and owning user.
type ResolvedBinding struct {
	UserID    string
	BindingID int64
	IsNew     bool
}

const (
	usersCollection             = "auth_users"
	containerRequestsCollection = "provisioner_container_requests"

	// bindingIDSequence names the counter that stands in for the AUTO_INCREMENT
	// that user_channels.binding_id was. The value is not internal: container
	// requests, the queued messages and the "binding-N" agent names all quote it,
	// so it has to stay an increasing integer rather than become an ObjectID.
	bindingIDSequence = "user_channels_binding_id"
)

// storedUserChannel is one platform identity linked to a user.
//
// It is embedded in the user document rather than kept in a collection of its
// own: the row and its owner are one thing, and the SQL that created a binding
// had to insert three tables inside a transaction. As an embedded element, the
// identity's uniqueness — the (channel, sender_id) primary key — becomes a
// multikey unique index, so a racing onboarding fails the whole insert instead
// of needing a rollback.
type storedUserChannel struct {
	BindingID     int64     `bson:"binding_id"`
	Channel       string    `bson:"channel"`
	SenderID      string    `bson:"sender_id"`
	Provider      string    `bson:"provider,omitempty"`
	Enabled       bool      `bson:"enabled"`
	BindingStatus string    `bson:"binding_status"`
	CreatedAt     time.Time `bson:"created_at"`
}

// storedUser is a user document.
//
// It carries the *identity* fields as well as the ones the bus itself uses. They
// used to live only in the portal's SQL table, which meant one account was known
// to two services through one shared table — and, once the bus moved off MySQL,
// that the two halves could disagree. The portal administers them through the
// bus's API now; the password hash is stored here and verified by the portal,
// which is where the login flow lives.
type storedUser struct {
	ID          string `bson:"_id"`
	DisplayName string `bson:"display_name,omitempty"`
	Status      string `bson:"status"`
	Tier        string `bson:"tier"`
	// Role is the administrative role (admin/operator/...); "user" when unset.
	Role string `bson:"role,omitempty"`
	// Email is unique when present; absent (or empty) means "no email", which is
	// why the index that enforces it is sparse rather than plain unique.
	Email string `bson:"email,omitempty"`
	// PasswordHash is a bcrypt hash produced by the portal. The bus stores and
	// returns it; it never sees the password itself.
	PasswordHash   string `bson:"password_hash,omitempty"`
	MustChangePass bool   `bson:"must_change_password,omitempty"`
	// PreferredLanguage is the language this person wants replies in, as a short
	// tag ("zh", "en"). Empty means "inherit the platform default"; it is the
	// top half of the two-level setting the language note is built from.
	PreferredLanguage string              `bson:"preferred_language,omitempty"`
	PasswordChanged   *time.Time          `bson:"password_changed_at,omitempty"`
	Channels          []storedUserChannel `bson:"channels,omitempty"`
	CreatedAt         time.Time           `bson:"created_at"`
	UpdatedAt         time.Time           `bson:"updated_at"`
}

// UserSummary is one row of the admin console's user list: identity and
// administration fields, never the credential.
type UserSummary struct {
	ID          string
	DisplayName string
	Email       string
	Role        string
	Status      string
	Tier        string
	// MustChangePassword is the flag the sign-in flow puts in its answer; the
	// console shows the same prompt. Never the credential itself.
	MustChangePassword bool
	// PreferredLanguage is "" when the account inherits the platform default.
	PreferredLanguage string
	UpdatedAt         time.Time
}

// storedContainerRequest is one provisioning request.
//
// Unlike the channel above it stays a document of its own: it has a lifecycle of
// its own, the runner polls it by status, and its count is not bounded by the
// user.
type storedContainerRequest struct {
	ID     bson.ObjectID `bson:"_id,omitempty"`
	UserID string        `bson:"user_id"`
	// Absent when the request is not bound to a channel: the SQL stored NULL
	// (migration 055), which is what lets a unique index permit many of them.
	BindingID  *int64     `bson:"binding_id,omitempty"`
	AgentID    string     `bson:"agent_id,omitempty"`
	Level      string     `bson:"level"`
	Status     string     `bson:"status"`
	RunnerID   string     `bson:"runner_id,omitempty"`
	DisposedAt *time.Time `bson:"disposed_at,omitempty"`
	CreatedAt  time.Time  `bson:"created_at"`
	UpdatedAt  time.Time  `bson:"updated_at"`
}

func (s *Store) users() *mongo.Collection {
	return s.db.Collection(usersCollection)
}

func (s *Store) containerRequests() *mongo.Collection {
	return s.db.Collection(containerRequestsCollection)
}

// channelIdentity is the element that names one platform identity, the pair the
// SQL's primary key covered.
func channelIdentity(channel, senderID string) bson.D {
	return bson.D{
		{Key: "channel", Value: channel},
		{Key: "sender_id", Value: senderID},
	}
}

// channelPath is the filter that finds the document holding an identity.
//
// Dotted paths rather than $elemMatch: an equality on channels.channel is what
// lets the unique index (which is partial on that path) serve the lookup. It can
// match a document whose channel and sender live in different elements, which is
// why every caller reads the array and checks the pair before trusting it.
func channelPath(channel, senderID string) bson.D {
	return bson.D{
		{Key: "channels.channel", Value: channel},
		{Key: "channels.sender_id", Value: senderID},
	}
}

// RegisterUser creates a new user in the registry. An empty displayName is
// stored as NULL and, on an existing user, never overwrites a stored name.
func (s *Store) RegisterUser(userID, displayName string) error {
	ctx, cancel := opCtx()
	defer cancel()

	now := time.Now().UTC()
	set := bson.D{{Key: "updated_at", Value: now}}
	if displayName != "" {
		// COALESCE(VALUES(display_name), display_name): a stored name survives a
		// registration that carries none.
		set = append(set, bson.E{Key: "display_name", Value: displayName})
	}
	update := bson.D{
		{Key: "$set", Value: set},
		{Key: "$setOnInsert", Value: bson.D{
			// The column defaults.
			{Key: "status", Value: "active"},
			{Key: "tier", Value: "free"},
			{Key: "created_at", Value: now},
		}},
	}
	err := retryOnDuplicateUpsert(func() error {
		_, err := s.users().UpdateOne(ctx, bson.D{{Key: "_id", Value: userID}}, update,
			options.UpdateOne().SetUpsert(true))
		return err
	})
	if err != nil {
		return fmt.Errorf("register user %q: %w", userID, err)
	}
	return nil
}

// LinkChannel associates a platform identity with a user.
func (s *Store) LinkChannel(userID, channel, senderID string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// The SQL was one INSERT ... ON DUPLICATE KEY UPDATE user_id = VALUES(user_id):
	// linking an identity that is already linked re-parents it, keeping its
	// binding id — container requests and the "binding-N" names quote that id, so
	// it cannot be reissued.
	owner, existing, err := s.channelOwner(ctx, channel, senderID)
	if err != nil {
		return fmt.Errorf("link channel %s/%s: %w", channel, senderID, err)
	}
	if owner == userID {
		return nil
	}

	element := storedUserChannel{
		Channel: channel, SenderID: senderID,
		Enabled: true, BindingStatus: "active", CreatedAt: time.Now().UTC(),
	}
	if existing != nil {
		element = *existing
	} else {
		element.BindingID, err = s.nextSequence(ctx, bindingIDSequence)
		if err != nil {
			return fmt.Errorf("link channel %s/%s: %w", channel, senderID, err)
		}
	}

	// A re-parented identity has to leave one document before it can join
	// another, and no transaction spans the two. So the order is: take it away,
	// then add it. Losing the process in between leaves the identity unlinked —
	// recoverable by linking again — rather than linked to two users, which the
	// unique index would then refuse to repair.
	if owner != "" {
		if _, err := s.users().UpdateOne(ctx,
			bson.D{
				{Key: "_id", Value: owner},
				{Key: "channels.channel", Value: channel},
				{Key: "channels.sender_id", Value: senderID},
			},
			bson.D{{Key: "$pull", Value: bson.D{{Key: "channels", Value: channelIdentity(channel, senderID)}}}},
		); err != nil {
			return fmt.Errorf("unlink channel %s/%s from user %q: %w", channel, senderID, owner, err)
		}
	}

	res, err := s.users().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: userID}},
		bson.D{{Key: "$push", Value: bson.D{{Key: "channels", Value: element}}}},
	)
	if err != nil {
		return fmt.Errorf("link channel %s/%s to user %q: %w", channel, senderID, userID, err)
	}
	if res.MatchedCount == 0 {
		// The SQL's foreign key refused this too: a channel belongs to a user
		// that exists.
		return fmt.Errorf("link channel %s/%s: user %q not found", channel, senderID, userID)
	}
	return nil
}

// channelOwner finds the user that holds a platform identity, and the element
// that holds it (nil when nobody does).
func (s *Store) channelOwner(ctx context.Context, channel, senderID string) (string, *storedUserChannel, error) {
	var doc storedUser
	err := s.users().FindOne(ctx, channelPath(channel, senderID),
		options.FindOne().SetProjection(bson.D{{Key: "channels", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("find channel owner: %w", err)
	}
	for i := range doc.Channels {
		if doc.Channels[i].Channel == channel && doc.Channels[i].SenderID == senderID {
			return doc.ID, &doc.Channels[i], nil
		}
	}
	return "", nil, nil
}

// ResolveUserBinding looks up or creates the binding for a platform identity.
func (s *Store) ResolveUserBinding(channel, senderID string) (ResolvedBinding, error) {
	userID, bindingID, enabled, err := s.lookupBinding(channel, senderID)
	if err == nil {
		if !enabled {
			return ResolvedBinding{}, ErrChannelDisabled
		}
		return ResolvedBinding{UserID: userID, BindingID: bindingID, IsNew: false}, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return ResolvedBinding{}, fmt.Errorf("resolve user %s/%s: %w", channel, senderID, err)
	}

	ctx, cancel := opCtx()
	defer cancel()

	newID := uuid.New().String()
	bindingID, err = s.nextSequence(ctx, bindingIDSequence)
	if err != nil {
		return ResolvedBinding{}, fmt.Errorf("allocate binding id: %w", err)
	}

	now := time.Now().UTC()
	doc := storedUser{
		ID: newID, Status: "pending", Tier: "free",
		Channels: []storedUserChannel{{
			BindingID: bindingID, Channel: channel, SenderID: senderID,
			Enabled: true, BindingStatus: "active", CreatedAt: now,
		}},
		CreatedAt: now, UpdatedAt: now,
	}

	// One insert. The uniqueness of the identity is a constraint on the embedded
	// array, so a racing onboarding fails the whole document: the SQL needed a
	// transaction to roll back the user row it had already written, this does not
	// — there is nothing to roll back.
	if _, err := s.users().InsertOne(ctx, doc); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			userID, bindingID, enabled, err2 := s.lookupBinding(channel, senderID)
			if err2 != nil {
				return ResolvedBinding{}, fmt.Errorf("retry resolve after race: %w", err2)
			}
			if !enabled {
				return ResolvedBinding{}, ErrChannelDisabled
			}
			return ResolvedBinding{UserID: userID, BindingID: bindingID, IsNew: false}, nil
		}
		return ResolvedBinding{}, fmt.Errorf("insert user: %w", err)
	}

	// The container request is a second document, and a standalone server cannot
	// put the two in one transaction. The order is deliberate: an error here
	// leaves a working binding whose container has not been asked for, and that
	// is the recoverable direction (the provisioning path asks again), whereas
	// the reverse would leave a request whose binding does not exist.
	if _, err := s.containerRequests().InsertOne(ctx, storedContainerRequest{
		UserID:    newID,
		BindingID: &bindingID,
		AgentID:   bindingAgentID(bindingID),
		Level:     "free",
		Status:    "pending",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return ResolvedBinding{}, fmt.Errorf("insert container request: %w", err)
	}

	return ResolvedBinding{UserID: newID, BindingID: bindingID, IsNew: true}, nil
}

// ResolveUser looks up the user ID for a given platform identity.
// If no user exists, it auto-creates one with status=pending and level=free,
// links the channel, and creates a container request.
func (s *Store) ResolveUser(channel, senderID string) (userID string, isNew bool, err error) {
	resolved, err := s.ResolveUserBinding(channel, senderID)
	if err != nil {
		return "", false, err
	}
	return resolved.UserID, resolved.IsNew, nil
}

// ListUsers returns all registered user IDs.
func (s *Store) ListUsers() ([]string, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.users().Find(ctx, bson.D{},
		options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}).
			SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	users := []string{}
	for cur.Next(ctx) {
		var doc struct {
			ID string `bson:"_id"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode user: %w", err)
		}
		users = append(users, doc.ID)
	}
	return users, cur.Err()
}

// UpdateUserStatus updates a user's status. It is idempotent: setting a user
// to a status they already hold is a no-op, not an error.
func (s *Store) UpdateUserStatus(userID, status string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// One statement rather than the SQL's COUNT followed by an UPDATE: the count
	// existed only to tell "no such user" from "already in that status", and an
	// update that matched nothing says the same thing without the race between
	// the two.
	res, err := s.users().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: userID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "status", Value: status},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("update user status: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SetUserPreferredLanguage stores the language this person wants replies in.
// An empty value clears it, which puts the account back on the platform default.
func (s *Store) SetUserPreferredLanguage(userID, language string) error {
	ctx, cancel := opCtx()
	defer cancel()

	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "preferred_language", Value: language},
		{Key: "updated_at", Value: time.Now().UTC()},
	}}}
	res, err := s.users().UpdateOne(ctx, bson.D{{Key: "_id", Value: userID}}, update)
	if err != nil {
		return fmt.Errorf("set preferred language: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrUserNotFound
	}
	return nil
}

// GetUserPreferredLanguage reads just the account's language choice. Empty
// means "inherit the platform default".
func (s *Store) GetUserPreferredLanguage(userID string) (string, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc struct {
		Language string `bson:"preferred_language"`
	}
	err := s.users().FindOne(ctx, bson.D{{Key: "_id", Value: userID}},
		options.FindOne().SetProjection(bson.D{{Key: "preferred_language", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", ErrUserNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get preferred language: %w", err)
	}
	return doc.Language, nil
}

// GetUserInfo returns the status and level for a user.
func (s *Store) GetUserInfo(userID string) (status, level string, err error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc struct {
		Status string `bson:"status"`
		Tier   string `bson:"tier"`
	}
	err = s.users().FindOne(ctx, bson.D{{Key: "_id", Value: userID}},
		options.FindOne().SetProjection(bson.D{
			{Key: "status", Value: 1}, {Key: "tier", Value: 1},
		})).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", "", ErrUserNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("get user info: %w", err)
	}
	return doc.Status, doc.Tier, nil
}

// setUserField writes one administration field, reporting an unknown user the
// way the SQL UPDATE did (nothing matched).
func (s *Store) setUserField(userID, field, value string) error {
	ctx, cancel := opCtx()
	defer cancel()

	res, err := s.users().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: userID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: field, Value: value},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("update user %s: %w", field, err)
	}
	if res.MatchedCount == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SetUserRole sets a user's administrative role.
func (s *Store) SetUserRole(userID, role string) error { return s.setUserField(userID, "role", role) }

// systemFlagsCollection holds one-shot per-installation flags. They are not
// account state, which is why they are not fields on any user.
const systemFlagsCollection = "sys_flags"

// bootstrapAdminFlag is the flag that says this installation has already given
// its administrator role to the first account created through a sign-up.
const bootstrapAdminFlag = "bootstrap_admin"

// ClaimBootstrapAdmin records that this installation has handed the
// administrator role to the account created by the first sign-up.
//
// The claim has to be atomic. Two registrations arriving together both see
// "no administrator yet", so counting is not enough to pick a winner; the flag
// document's _id is. Exactly one upsert creates it, and that caller is told it
// won. Every later registration finds the document already there and loses.
//
// The claim is one-shot per installation: it is never released automatically
// when an administrator is deleted or demoted. Recovery from that is a
// deliberate act by someone with shell access, not an invitation for the next
// stranger who registers.
func (s *Store) ClaimBootstrapAdmin(userID string) (bool, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var res *mongo.UpdateResult
	err := retryOnDuplicateUpsert(func() error {
		r, err := s.db.Collection(systemFlagsCollection).UpdateOne(ctx,
			bson.D{{Key: "_id", Value: bootstrapAdminFlag}},
			bson.D{{Key: "$setOnInsert", Value: bson.D{
				{Key: "user_id", Value: userID},
				{Key: "claimed_at", Value: time.Now().UTC()},
			}}},
			options.UpdateOne().SetUpsert(true),
		)
		res = r
		return err
	})
	if err != nil {
		return false, fmt.Errorf("claim bootstrap administrator: %w", err)
	}
	return res != nil && res.UpsertedCount == 1, nil
}

// ReleaseBootstrapAdmin hands the claim back after the role could not be
// written, so the next registration gets the same chance this one did. It only
// removes the flag the same account created.
func (s *Store) ReleaseBootstrapAdmin(userID string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if _, err := s.db.Collection(systemFlagsCollection).DeleteOne(ctx,
		bson.D{{Key: "_id", Value: bootstrapAdminFlag}, {Key: "user_id", Value: userID}},
	); err != nil {
		return fmt.Errorf("release bootstrap administrator: %w", err)
	}
	return nil
}

// SetUserTier sets a user's resource tier.
func (s *Store) SetUserTier(userID, tier string) error { return s.setUserField(userID, "tier", tier) }

// SetUserPasswordKeepingFlag replaces the hash and leaves the forced-change flag
// alone.
//
// The directory path wipes a local password when an account becomes
// directory-managed; the flag belongs to whatever password was there before and
// says nothing about a blank one. Stamping it either way would be inventing a
// decision the caller did not make.
func (s *Store) SetUserPasswordKeepingFlag(userID, passwordHash string) error {
	ctx, cancel := opCtx()
	defer cancel()

	now := time.Now().UTC()
	res, err := s.users().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: userID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "password_hash", Value: passwordHash},
			{Key: "password_changed_at", Value: now},
			{Key: "updated_at", Value: now},
		}}},
	)
	if err != nil {
		return fmt.Errorf("set user password: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrUserNotFound
	}
	return nil
}

// SetUserPassword stores a password hash produced by the caller.
//
// The hash never leaves this process except back to the caller that owns the
// login flow (UserPasswordHash), and the bus never sees a password: verification
// stays where the browser-facing form is.
func (s *Store) SetUserPassword(userID, passwordHash string, mustChange bool) error {
	ctx, cancel := opCtx()
	defer cancel()

	now := time.Now().UTC()
	res, err := s.users().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: userID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "password_hash", Value: passwordHash},
			{Key: "must_change_password", Value: mustChange},
			// Stamped here rather than by the caller, so "when did this password
			// last change" cannot be forgotten on one of the two call paths.
			{Key: "password_changed_at", Value: now},
			{Key: "updated_at", Value: now},
		}}},
	)
	if err != nil {
		return fmt.Errorf("set user password: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrUserNotFound
	}
	return nil
}

// UserPasswordHash returns the stored hash, or "" when the user has none.
func (s *Store) UserPasswordHash(userID string) (string, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc struct {
		PasswordHash string `bson:"password_hash"`
	}
	err := s.users().FindOne(ctx, bson.D{{Key: "_id", Value: userID}},
		options.FindOne().SetProjection(bson.D{{Key: "password_hash", Value: 1}})).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get user password hash: %w", err)
	}
	return doc.PasswordHash, nil
}

// UserExists reports whether the account is there, whatever its status.
func (s *Store) UserExists(userID string) (bool, error) {
	ctx, cancel := opCtx()
	defer cancel()

	count, err := s.users().CountDocuments(ctx, bson.D{{Key: "_id", Value: userID}})
	if err != nil {
		return false, fmt.Errorf("check user: %w", err)
	}
	return count > 0, nil
}

// CountActiveAdmins counts accounts that may administer the platform: role admin
// and not deleted. The portal asks before demoting or deleting one, so the last
// admin cannot lock everybody out.
func (s *Store) CountActiveAdmins() (int, error) {
	ctx, cancel := opCtx()
	defer cancel()

	count, err := s.users().CountDocuments(ctx, bson.D{
		{Key: "role", Value: "admin"},
		{Key: "status", Value: bson.D{{Key: "$ne", Value: "deleted"}}},
	})
	if err != nil {
		return 0, fmt.Errorf("count active admins: %w", err)
	}
	return int(count), nil
}

// CountActiveUsers counts the accounts the console calls active. It is the
// overview's headline number, and it is a count rather than a list because that
// number has to stay cheap as the table grows.
func (s *Store) CountActiveUsers() (int, error) {
	ctx, cancel := opCtx()
	defer cancel()

	count, err := s.users().CountDocuments(ctx, bson.D{{Key: "status", Value: "active"}})
	if err != nil {
		return 0, fmt.Errorf("count active users: %w", err)
	}
	return int(count), nil
}

// CountUsersByTier counts the accounts per resource tier.
//
// The three tiers are always present in the result, zero included, because the
// console draws a fixed set of columns and an absent key would read as "not
// counted" rather than "none".
func (s *Store) CountUsersByTier() (map[string]int, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.users().Aggregate(ctx, bson.A{
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$tier"},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	})
	if err != nil {
		return nil, fmt.Errorf("count users by tier: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := map[string]int{"free": 0, "pro": 0, "ultra": 0}
	for cur.Next(ctx) {
		var doc struct {
			Tier  string `bson:"_id"`
			Count int    `bson:"count"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode tier count: %w", err)
		}
		out[doc.Tier] = doc.Count
	}
	return out, cur.Err()
}

// GetUserAccount returns one account the way the console shows it: identity and
// administration fields, with the role defaulted the way the SQL default was.
func (s *Store) GetUserAccount(userID string) (*UserSummary, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedUser
	err := s.users().FindOne(ctx, bson.D{{Key: "_id", Value: userID}},
		options.FindOne().SetProjection(bson.D{
			{Key: "display_name", Value: 1}, {Key: "status", Value: 1},
			{Key: "tier", Value: 1}, {Key: "role", Value: 1}, {Key: "updated_at", Value: 1},
			{Key: "email", Value: 1}, {Key: "must_change_password", Value: 1},
			{Key: "preferred_language", Value: 1},
		})).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user account: %w", err)
	}
	role := doc.Role
	if role == "" {
		role = "user"
	}
	return &UserSummary{
		ID: doc.ID, DisplayName: doc.DisplayName, Email: doc.Email, Role: role,
		Status: doc.Status, Tier: doc.Tier, UpdatedAt: doc.UpdatedAt,
		MustChangePassword: doc.MustChangePass,
		PreferredLanguage:  doc.PreferredLanguage,
	}, nil
}

// FindUserByEmail returns an account by its e-mail address.
//
// It exists for the sign-in path, which starts from what the person typed rather
// than from an id. The address is unique (a sparse unique index: accounts without
// one are allowed, two with the same one are not), so this is a single lookup.
func (s *Store) FindUserByEmail(email string) (*UserSummary, error) {
	ctx, cancel := opCtx()
	defer cancel()

	if email == "" {
		return nil, ErrUserNotFound
	}
	var doc storedUser
	err := s.users().FindOne(ctx, bson.D{{Key: "email", Value: email}},
		options.FindOne().SetProjection(bson.D{
			{Key: "display_name", Value: 1}, {Key: "status", Value: 1},
			{Key: "tier", Value: 1}, {Key: "role", Value: 1}, {Key: "updated_at", Value: 1},
			{Key: "email", Value: 1}, {Key: "must_change_password", Value: 1},
			{Key: "preferred_language", Value: 1},
		})).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find user by email: %w", err)
	}
	role := doc.Role
	if role == "" {
		role = "user"
	}
	return &UserSummary{
		ID: doc.ID, DisplayName: doc.DisplayName, Email: doc.Email, Role: role,
		Status: doc.Status, Tier: doc.Tier, UpdatedAt: doc.UpdatedAt,
		MustChangePassword: doc.MustChangePass,
		PreferredLanguage:  doc.PreferredLanguage,
	}, nil
}

// SetUserDisplayName renames an account.
func (s *Store) SetUserDisplayName(userID, displayName string) error {
	return s.setUserField(userID, "display_name", displayName)
}

// ErrEmailTaken is returned when an address already belongs to an account. The
// unique index on `email` is sparse, so accounts without one are unaffected.
var ErrEmailTaken = errors.New("email already in use")

// SetUserEmail links an address to an account.
//
// The sign-in path looks accounts up by address, so two accounts cannot share
// one: the sparse unique index enforces it and this maps the duplicate-key error
// to something a caller can answer 409 with.
func (s *Store) SetUserEmail(userID, email string) error {
	ctx, cancel := opCtx()
	defer cancel()

	res, err := s.users().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: userID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "email", Value: email},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
	)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return ErrEmailTaken
		}
		return fmt.Errorf("set user email: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrUserNotFound
	}
	return nil
}

// UserChannels returns the platform identities linked to a user, by channel name.
func (s *Store) UserChannels(userID string) ([]string, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedUser
	err := s.users().FindOne(ctx, bson.D{{Key: "_id", Value: userID}},
		options.FindOne().SetProjection(bson.D{{Key: "channels", Value: 1}})).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user channels: %w", err)
	}
	out := make([]string, 0, len(doc.Channels))
	for _, c := range doc.Channels {
		out = append(out, c.Channel)
	}
	return out, nil
}

// ListUserSummaries returns the admin console's user list, newest first.
func (s *Store) ListUserSummaries(includeDeleted bool, limit int) ([]UserSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	ctx, cancel := opCtx()
	defer cancel()

	filter := bson.D{}
	if !includeDeleted {
		filter = bson.D{{Key: "status", Value: bson.D{{Key: "$ne", Value: "deleted"}}}}
	}
	cur, err := s.users().Find(ctx, filter,
		options.Find().
			// Only the columns the list shows: the credential and the channels are
			// not among them.
			SetProjection(bson.D{
				{Key: "display_name", Value: 1}, {Key: "status", Value: 1},
				{Key: "tier", Value: 1}, {Key: "role", Value: 1}, {Key: "updated_at", Value: 1},
				{Key: "email", Value: 1},
			}).
			SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: 1}}).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []UserSummary{}
	for cur.Next(ctx) {
		var doc storedUser
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode user: %w", err)
		}
		role := doc.Role
		if role == "" {
			// The SQL default was 'user', and the console shows a role for every
			// row.
			role = "user"
		}
		out = append(out, UserSummary{
			ID: doc.ID, DisplayName: doc.DisplayName, Email: doc.Email, Role: role,
			Status: doc.Status, Tier: doc.Tier, UpdatedAt: doc.UpdatedAt,
		})
	}
	return out, cur.Err()
}

// SearchUserSummaries finds live accounts by display name or e-mail fragment.
// It is what a member picker searches with: the caller types a couple of
// characters and gets a short list, never the whole table.
//
// The fragment is quoted before it becomes a pattern — a name containing "." or
// "+" must match itself rather than behave as a regular expression.
func (s *Store) SearchUserSummaries(q string, limit int) ([]UserSummary, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []UserSummary{}, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	ctx, cancel := opCtx()
	defer cancel()

	pattern := bson.D{
		{Key: "$regex", Value: regexp.QuoteMeta(q)},
		{Key: "$options", Value: "i"},
	}
	filter := bson.D{
		{Key: "status", Value: bson.D{{Key: "$ne", Value: "deleted"}}},
		{Key: "$or", Value: bson.A{
			bson.D{{Key: "display_name", Value: pattern}},
			bson.D{{Key: "email", Value: pattern}},
		}},
	}
	cur, err := s.users().Find(ctx, filter,
		options.Find().
			// Same projection as the list: identity only, never the credential.
			SetProjection(bson.D{
				{Key: "display_name", Value: 1}, {Key: "status", Value: 1},
				{Key: "tier", Value: 1}, {Key: "role", Value: 1}, {Key: "updated_at", Value: 1},
				{Key: "email", Value: 1},
			}).
			SetSort(bson.D{{Key: "display_name", Value: 1}, {Key: "_id", Value: 1}}).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("search users: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []UserSummary{}
	for cur.Next(ctx) {
		var doc storedUser
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode user: %w", err)
		}
		role := doc.Role
		if role == "" {
			role = "user"
		}
		out = append(out, UserSummary{
			ID: doc.ID, DisplayName: doc.DisplayName, Email: doc.Email, Role: role,
			Status: doc.Status, Tier: doc.Tier, UpdatedAt: doc.UpdatedAt,
		})
	}
	return out, cur.Err()
}

// UserIDForBinding returns the user a binding belongs to ("" when unowned).
func (s *Store) UserIDForBinding(bindingID int64) (string, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc struct {
		ID string `bson:"_id"`
	}
	err := s.users().FindOne(ctx,
		bson.D{{Key: "channels.binding_id", Value: bindingID}},
		options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get binding owner: %w", err)
	}
	return doc.ID, nil
}

// CreateContainerRequest enqueues a container provisioning request for a user.
// A user may only have one non-terminal request at a time per binding; a
// bindingID of 0 means the user-level container shared by all their agents.
// Agent-initiated requests store no binding so the unique index on binding_id
// does not collide across users. When a previously disposed request still
// occupies the slot, it is revived instead of inserting a new row.
func (s *Store) CreateContainerRequest(userID string, bindingID int64, agentID, level string) error {
	if level == "" {
		level = "free"
	}
	ctx, cancel := opCtx()
	defer cancel()

	identity := containerIdentity(userID, bindingID)

	// One non-terminal request per (user, binding); agent-level dedup is
	// deliberately not used because all of a user's agents share one container.
	existing, err := s.containerRequests().CountDocuments(ctx, append(identity,
		bson.E{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{
			"pending", "creating", "running", "suspended", "disposing",
		}}}}))
	if err != nil {
		return fmt.Errorf("check existing container request: %w", err)
	}
	if existing > 0 {
		return ErrContainerAlreadyRequested
	}

	now := time.Now().UTC()
	// A disposed request still holds the slot; revive it rather than inserting a
	// new row.
	revived, err := s.containerRequests().UpdateMany(ctx,
		append(identity, bson.E{Key: "status", Value: "disposed"}),
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "status", Value: "pending"},
			{Key: "level", Value: level},
			{Key: "agent_id", Value: agentID},
			{Key: "updated_at", Value: now},
		}}, {Key: "$unset", Value: bson.D{{Key: "runner_id", Value: ""}, {Key: "disposed_at", Value: ""}}}},
	)
	if err != nil {
		return fmt.Errorf("revive disposed container request: %w", err)
	}
	if revived.MatchedCount > 0 {
		return nil
	}

	if _, err := s.containerRequests().InsertOne(ctx, storedContainerRequest{
		UserID:    userID,
		BindingID: nullableInt64(bindingID),
		AgentID:   agentID,
		Level:     level,
		Status:    "pending",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		return fmt.Errorf("insert container request: %w", err)
	}
	return nil
}

// containerIdentity is the filter naming the request a (user, binding) pair owns.
// A binding of 0 means the user-level container, which the SQL stored as NULL —
// and a null comparison also matches a document without the field.
func containerIdentity(userID string, bindingID int64) bson.D {
	return bson.D{
		{Key: "user_id", Value: userID},
		{Key: "binding_id", Value: nullableInt64(bindingID)},
	}
}

// EnsureContainerRequest enqueues a pending container provisioning request for
// a user unless one is already active, using the user's tier as the resource
// level. It is idempotent: if a non-disposed request already exists it returns
// nil instead of ErrContainerAlreadyRequested.
func (s *Store) EnsureContainerRequest(userID, agentID string) error {
	_, tier, err := s.GetUserInfo(userID)
	if err != nil {
		return err
	}
	if tier == "" {
		tier = "free"
	}

	if err := s.CreateContainerRequest(userID, 0, agentID, tier); err != nil {
		if errors.Is(err, ErrContainerAlreadyRequested) {
			return nil
		}
		return err
	}
	return nil
}

// LastActivityByUser returns the newest message each user has queued, in either
// direction.
//
// The console's user list shows "last seen", which the platform knows only through
// the queues: a message in flight is the last moment a user did anything. One
// aggregation per queue, merged here rather than in the caller — the caller would
// otherwise need to know there are two of them.
func (s *Store) LastActivityByUser() (map[string]time.Time, error) {
	ctx, cancel := opCtx()
	defer cancel()

	out := map[string]time.Time{}
	for _, coll := range []func() *mongo.Collection{s.inboundQueue, s.outboundQueue} {
		cur, err := coll().Aggregate(ctx, bson.A{
			bson.D{{Key: "$group", Value: bson.D{
				{Key: "_id", Value: "$user_id"},
				{Key: "last", Value: bson.D{{Key: "$max", Value: "$created_at"}}},
			}}},
		})
		if err != nil {
			return nil, fmt.Errorf("last activity: %w", err)
		}
		for cur.Next(ctx) {
			var doc struct {
				UserID string    `bson:"_id"`
				Last   time.Time `bson:"last"`
			}
			if err := cur.Decode(&doc); err != nil {
				_ = cur.Close(ctx)
				return nil, fmt.Errorf("decode last activity: %w", err)
			}
			if doc.UserID == "" {
				continue
			}
			if prev, ok := out[doc.UserID]; !ok || doc.Last.After(prev) {
				out[doc.UserID] = doc.Last
			}
		}
		err = cur.Err()
		_ = cur.Close(ctx)
		if err != nil {
			return nil, fmt.Errorf("last activity: %w", err)
		}
	}
	return out, nil
}

// ContainerSummary is one row of the console's container view.
//
// It carries the runner because the runtime snapshot shows which host a container
// landed on — a field the protocol's ContainerRequest does not have, since the
// agent-facing shape has no reason to know.
type ContainerSummary struct {
	ID        string
	UserID    string
	Level     string
	Status    string
	RunnerID  string
	UpdatedAt time.Time
}

// ListContainerSummaries returns container requests newest first. An empty userID
// means every user's — the runtime snapshot's view.
func (s *Store) ListContainerSummaries(userID string, limit int) ([]ContainerSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	ctx, cancel := opCtx()
	defer cancel()

	filter := bson.D{}
	if userID != "" {
		filter = bson.D{{Key: "user_id", Value: userID}}
	}
	cur, err := s.containerRequests().Find(ctx, filter,
		options.Find().
			SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "_id", Value: -1}}).
			SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []ContainerSummary{}
	for cur.Next(ctx) {
		var doc storedContainerRequest
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode container request: %w", err)
		}
		out = append(out, ContainerSummary{
			ID: doc.ID.Hex(), UserID: doc.UserID, Level: doc.Level,
			Status: doc.Status, RunnerID: doc.RunnerID, UpdatedAt: doc.UpdatedAt,
		})
	}
	return out, cur.Err()
}

// CountContainersByStatus counts the requests per status. Only the statuses that
// occur are returned: the caller adds up the buckets it cares about, and a status
// added later does not have to be enumerated here.
func (s *Store) CountContainersByStatus() (map[string]int, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.containerRequests().Aggregate(ctx, bson.A{
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$status"},
			{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
		}}},
	})
	if err != nil {
		return nil, fmt.Errorf("count containers: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := map[string]int{}
	for cur.Next(ctx) {
		var doc struct {
			Status string `bson:"_id"`
			Count  int    `bson:"count"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode container count: %w", err)
		}
		out[doc.Status] = doc.Count
	}
	return out, cur.Err()
}

// ListContainers returns a user's container requests, most recent first.
func (s *Store) ListContainers(userID string, limit int) ([]nanoprotocol.ContainerRequest, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	ctx, cancel := opCtx()
	defer cancel()

	// _id breaks the tie: created_at has millisecond resolution, and rows written
	// together must still come back newest-first in a stable order.
	cur, err := s.containerRequests().Find(ctx,
		bson.D{{Key: "user_id", Value: userID}},
		options.Find().
			SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}).
			SetLimit(int64(limit)),
	)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	requests := []nanoprotocol.ContainerRequest{}
	for cur.Next(ctx) {
		var doc storedContainerRequest
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode container request: %w", err)
		}
		requests = append(requests, doc.request())
	}
	return requests, cur.Err()
}

func (d storedContainerRequest) request() nanoprotocol.ContainerRequest {
	req := nanoprotocol.ContainerRequest{
		UserID: d.UserID, AgentID: d.AgentID,
		Level: d.Level, Status: d.Status,
	}
	if d.BindingID != nil {
		req.BindingID = *d.BindingID
	}
	return req
}

// PollPendingContainers returns up to 10 pending or disposing container requests and
// claims them for the runner. Pending requests are claimed by moving to 'creating';
// disposing requests are claimed by assigning the runner (status stays 'disposing').
func (s *Store) PollPendingContainers(runnerID string) ([]nanoprotocol.ContainerRequest, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.containerRequests().Find(ctx,
		bson.D{{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{"pending", "disposing", "restarting"}}}}},
		options.Find().
			SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}).
			SetLimit(10),
	)
	if err != nil {
		return nil, fmt.Errorf("query pending containers: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	var candidates []storedContainerRequest
	for cur.Next(ctx) {
		var doc storedContainerRequest
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode container request: %w", err)
		}
		candidates = append(candidates, doc)
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("iterate container requests: %w", err)
	}

	requests := make([]nanoprotocol.ContainerRequest, 0, len(candidates))
	for _, doc := range candidates {
		// The claim is conditional on the status the poll saw. The SQL got that
		// from FOR UPDATE (the rows were locked for the whole transaction); here
		// the condition is what makes two runners racing for the same request
		// resolve to one winner, and a request this runner did not win is left
		// out of the answer rather than handed to a runner that does not own it.
		claimed, err := s.claimContainerRequest(ctx, doc, runnerID)
		if err != nil {
			return nil, err
		}
		if !claimed {
			continue
		}
		// The display name is the user's (the SQL joined users for it); with the
		// user in its own collection this is a second read, for at most ten
		// requests.
		request := doc.request()
		name, err := s.displayNameOf(ctx, doc.UserID)
		if err != nil {
			return nil, err
		}
		request.DisplayName = name
		requests = append(requests, request)
	}
	return requests, nil
}

// claimContainerRequest claims one request for a runner, returning false when the
// status moved since it was read (someone else won it).
func (s *Store) claimContainerRequest(ctx context.Context, doc storedContainerRequest, runnerID string) (bool, error) {
	filter := bson.D{
		{Key: "_id", Value: doc.ID},
		// The status as it was read: a conditional claim cannot take a request
		// that has since moved on.
		{Key: "status", Value: doc.Status},
	}
	set := bson.D{{Key: "runner_id", Value: runnerID}}
	if doc.Status == "pending" {
		set = append(set, bson.E{Key: "status", Value: "creating"})
	}

	res, err := s.containerRequests().UpdateOne(ctx, filter, bson.D{
		{Key: "$set", Value: set},
		// A no-op update still counts as modified here only when something
		// changed, so the claim is expressed as matched-or-not.
	})
	if err != nil {
		return false, fmt.Errorf("claim container request %s: %w", doc.UserID, err)
	}
	return res.MatchedCount > 0, nil
}

// displayNameOf reads the account name a container runs as.
func (s *Store) displayNameOf(ctx context.Context, userID string) (string, error) {
	var doc struct {
		DisplayName string `bson:"display_name"`
	}
	err := s.users().FindOne(ctx, bson.D{{Key: "_id", Value: userID}},
		options.FindOne().SetProjection(bson.D{{Key: "display_name", Value: 1}})).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read container owner %s: %w", userID, err)
	}
	return doc.DisplayName, nil
}

// DisposeContainerRequest marks a user's active container request as 'disposing'
// so nanorunner will deprovision it. Returns ErrContainerNotFound when no
// active request exists.
func (s *Store) DisposeContainerRequest(userID string, bindingID int64) error {
	ctx, cancel := opCtx()
	defer cancel()

	filter := containerIdentity(userID, bindingID)
	if bindingID > 0 {
		// A binding identifies the request on its own, as the SQL's
		// `WHERE binding_id = ?` did.
		filter = bson.D{{Key: "binding_id", Value: bindingID}}
	}
	// 'disposing' is excluded as well as 'disposed', which is what the SQL's
	// `status NOT IN ('disposed')` amounted to: it matched the row and then
	// changed nothing (no column value moved, so ON UPDATE never fired and
	// RowsAffected stayed 0), which the caller read as "nothing to dispose".
	// Excluding it states the same rule instead of relying on that.
	filter = append(filter, bson.E{Key: "status", Value: bson.D{{Key: "$nin", Value: bson.A{"disposing", "disposed"}}}})

	res, err := s.containerRequests().UpdateMany(ctx, filter,
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "status", Value: "disposing"},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("dispose container request: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrContainerNotFound
	}
	return nil
}

// RestartContainerRequest marks a running container request as 'restarting' so
// nanorunner deprovisions and reprovisions it in place. Like reprovision, the
// rebuilt container follows the user's current tier rather than the level
// recorded at the previous provision. Returns ErrContainerNotFound when no
// running request exists.
func (s *Store) RestartContainerRequest(userID string) error {
	ctx, cancel := opCtx()
	defer cancel()

	tier, found, err := s.tier(ctx, userID)
	if err != nil {
		return err
	}
	if !found {
		// The SQL joined users, so a missing user matched no row at all.
		return ErrContainerNotFound
	}

	set := bson.D{
		{Key: "status", Value: "restarting"},
		{Key: "updated_at", Value: time.Now().UTC()},
	}
	// COALESCE(u.tier, cr.level): an empty tier leaves the stored level alone.
	if tier != "" {
		set = append(set, bson.E{Key: "level", Value: tier})
	}

	res, err := s.containerRequests().UpdateMany(ctx,
		bson.D{{Key: "user_id", Value: userID}, {Key: "status", Value: "running"}},
		bson.D{{Key: "$set", Value: set}},
	)
	if err != nil {
		return fmt.Errorf("restart container request: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrContainerNotFound
	}
	return nil
}

// ReprovisionContainerRequest revives a user's disposed or failed container
// request so nanorunner provisions a fresh container from the current agent
// image. The stored level is refreshed from the user's current tier: the last
// provision may predate a tier change, and the runner sizes the container from
// this level. Returns ErrContainerNotFound when there is nothing to revive.
func (s *Store) ReprovisionContainerRequest(userID string) error {
	ctx, cancel := opCtx()
	defer cancel()

	tier, found, err := s.tier(ctx, userID)
	if err != nil {
		return err
	}
	if !found {
		return ErrContainerNotFound
	}

	set := bson.D{
		{Key: "status", Value: "pending"},
		{Key: "updated_at", Value: time.Now().UTC()},
	}
	if tier != "" {
		set = append(set, bson.E{Key: "level", Value: tier})
	}

	res, err := s.containerRequests().UpdateMany(ctx,
		bson.D{
			{Key: "user_id", Value: userID},
			{Key: "status", Value: bson.D{{Key: "$in", Value: bson.A{"disposed", "failed"}}}},
		},
		bson.D{
			{Key: "$set", Value: set},
			{Key: "$unset", Value: bson.D{{Key: "runner_id", Value: ""}, {Key: "disposed_at", Value: ""}}},
		},
	)
	if err != nil {
		return fmt.Errorf("reprovision container request: %w", err)
	}
	if res.MatchedCount == 0 {
		return ErrContainerNotFound
	}
	return nil
}

// tier reads a user's tier, and whether the user exists at all.
func (s *Store) tier(ctx context.Context, userID string) (string, bool, error) {
	var doc struct {
		Tier string `bson:"tier"`
	}
	err := s.users().FindOne(ctx, bson.D{{Key: "_id", Value: userID}},
		options.FindOne().SetProjection(bson.D{{Key: "tier", Value: 1}})).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read user tier: %w", err)
	}
	return doc.Tier, true, nil
}

// UpdateContainerStatus updates the status of a container request.
// Once a container is being disposed or already disposed, only transitions to
// 'disposed' are honored; other updates are ignored so a deprovision cannot be
// overridden by a racing provision completion.
func (s *Store) UpdateContainerStatus(userID string, bindingID int64, status string) error {
	ctx, cancel := opCtx()
	defer cancel()

	filter := containerIdentity(userID, bindingID)
	if bindingID > 0 {
		filter = bson.D{{Key: "binding_id", Value: bindingID}}
	}
	// `status IS NOT NULL` on the way to 'disposed' was true for every row — the
	// deprovision's own verdict is never blocked.
	if status != "disposed" {
		filter = append(filter, bson.E{Key: "status", Value: bson.D{{Key: "$nin", Value: bson.A{"disposing", "disposed"}}}})
	}

	// Matched rather than modified: MySQL's RowsAffected counts *changed* rows,
	// so the SQL reported "not found" for a status that was already set. Nothing
	// depends on that accident, and a caller that asks for the status a request
	// already has should not be told the request is missing.
	res, err := s.containerRequests().UpdateMany(ctx, filter,
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "status", Value: status},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("update container status: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("container request not found for user %s binding %d", userID, bindingID)
	}
	return nil
}

func (s *Store) lookupBinding(channel, senderID string) (userID string, bindingID int64, enabled bool, err error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedUser
	err = s.users().FindOne(ctx, channelPath(channel, senderID),
		options.FindOne().SetProjection(bson.D{{Key: "channels", Value: 1}}),
	).Decode(&doc)
	if err != nil {
		return "", 0, false, err
	}
	for _, c := range doc.Channels {
		if c.Channel == channel && c.SenderID == senderID {
			return doc.ID, c.BindingID, c.Enabled, nil
		}
	}
	// The element the filter matched is gone: the identity was unlinked between
	// the two reads, which is the SQL's ErrNoRows.
	return "", 0, false, mongo.ErrNoDocuments
}

func bindingAgentID(bindingID int64) string {
	return "binding-" + strconv.FormatInt(bindingID, 10)
}

// EnableChannel enables a channel for a user.
func (s *Store) EnableChannel(userID, channel string) error {
	return s.setChannelEnabled(userID, channel, true)
}

// DisableChannel disables a channel for a user.
func (s *Store) DisableChannel(userID, channel string) error {
	return s.setChannelEnabled(userID, channel, false)
}

func (s *Store) setChannelEnabled(userID, channel string, enabled bool) error {
	ctx, cancel := opCtx()
	defer cancel()

	// The positional $ names the element the filter matched, so this stays one
	// update of one document.
	_, err := s.users().UpdateOne(ctx,
		bson.D{
			{Key: "_id", Value: userID},
			{Key: "channels.channel", Value: channel},
		},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "channels.$.enabled", Value: enabled},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("set channel enabled: %w", err)
	}
	return nil
}
