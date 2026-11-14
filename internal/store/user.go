package store

// The account domain, as the bus sees it.
//
// The account document, its channel links, its credential and its one-shot
// administrator hand-out are the auth service's. The bus still serves the endpoints
// the console, the gateway and the runtime call; those handlers keep calling these
// methods, and the methods ask the module that owns the rows (accounts_remote.go).
// Nothing here touches `auth_users`.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ErrUserNotFound is returned when a user cannot be resolved.
var ErrUserNotFound = errors.New("user not found")

// ErrEmailTaken reports that an account with the same address already exists.
var ErrEmailTaken = errors.New("email already taken")

// ErrChannelDisabled is returned when a user's channel is disabled.
var ErrChannelDisabled = errors.New("channel disabled")

// ErrContainerAlreadyRequested is returned when a container is already pending for the user.
var ErrContainerAlreadyRequested = errors.New("container already requested")

// ErrContainerNotFound is returned when no active container request exists to dispose.
var ErrContainerNotFound = errors.New("container request not found")

// ResolvedBinding describes the resolved binding and owning user.
type ResolvedBinding struct {
	UserID    string
	BindingID int64
	IsNew     bool
}

// UserSummary is one row of the admin console's user list: identity and
// administration fields, never the credential.
type UserSummary struct {
	ID                 string
	DisplayName        string
	Email              string
	Role               string
	Status             string
	Tier               string
	MustChangePassword bool
	PreferredLanguage  string
	UpdatedAt          time.Time
}

// RegisterUser creates a new user in the registry. An empty displayName never
// overwrites a stored name.
func (s *Store) RegisterUser(userID, displayName string) error {
	backend, err := s.accountBackend()
	if err != nil {
		return err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.Register(ctx, userID, displayName)
}

// LinkChannel associates a platform identity with a user (moving it if it was
// linked elsewhere; the binding id survives the move).
func (s *Store) LinkChannel(userID, channel, senderID string) error {
	backend, err := s.accountBackend()
	if err != nil {
		return err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.LinkChannel(ctx, userID, channel, senderID)
}

// ResolveUserBinding looks up or creates the binding for a platform identity, and
// asks the provider for a container when the account is new.
func (s *Store) ResolveUserBinding(channel, senderID string) (ResolvedBinding, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return ResolvedBinding{}, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	resolved, err := backend.Resolve(ctx, channel, senderID)
	if err != nil {
		return ResolvedBinding{}, fmt.Errorf("resolve user %s/%s: %w", channel, senderID, err)
	}
	if !resolved.IsNew {
		return resolved, nil
	}

	// A new account has no agent: the console creates the first one, and a channel
	// identity never opens the console. Ask the agent rows' owner for a default
	// one first, so the container that is about to be provisioned has something to
	// run. A failure is logged rather than fatal — the account and the binding are
	// still useful, and the console can create the agent by hand — but it means
	// this user will not be answered until one exists.
	agentID := ""
	if s.agents != nil && s.agents.Enabled() {
		ensureCtx, cancelEnsure := accountContext()
		agentID, err = s.agents.EnsureDefaultAgent(ensureCtx, resolved.UserID)
		cancelEnsure()
		if err != nil {
			slog.Warn("could not create the new account's default agent",
				"user_id", resolved.UserID, "binding_id", resolved.BindingID, "error", err)
			agentID = ""
		}
	}
	if agentID == "" {
		agentID = bindingAgentID(resolved.BindingID)
	}

	// The container request is the provisioning queue's, not identity's, so the
	// onboarding path asks its owner. A failure here leaves a working binding whose
	// container has not been asked for — the recoverable direction, because the
	// provisioning path asks again.
	if s.containers != nil && s.containers.Enabled() {
		enqueueCtx, cancelEnqueue := accountContext()
		defer cancelEnqueue()
		if err := s.containers.Enqueue(enqueueCtx, resolved.UserID, resolved.BindingID, agentID, "free"); err != nil {
			return ResolvedBinding{}, fmt.Errorf("enqueue container for %s: %w", resolved.UserID, err)
		}
	}
	return resolved, nil
}

// ResolveUser looks up the user ID for a given platform identity, creating the
// account (and its container request) when it is new.
func (s *Store) ResolveUser(channel, senderID string) (userID string, isNew bool, err error) {
	resolved, err := s.ResolveUserBinding(channel, senderID)
	if err != nil {
		return "", false, err
	}
	return resolved.UserID, resolved.IsNew, nil
}

// ListUsers returns all registered user IDs.
func (s *Store) ListUsers() ([]string, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return nil, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.AllUserIDs(ctx)
}

// UpdateUserStatus updates a user's status. It is idempotent.
func (s *Store) UpdateUserStatus(userID, status string) error {
	return s.setUserField(userID, AccountPatch{Status: &status})
}

// SetUserRole sets the administrative role.
func (s *Store) SetUserRole(userID, role string) error {
	return s.setUserField(userID, AccountPatch{Role: &role})
}

// SetUserTier sets the account's plan.
func (s *Store) SetUserTier(userID, tier string) error {
	return s.setUserField(userID, AccountPatch{Tier: &tier})
}

// SetUserDisplayName renames an account.
func (s *Store) SetUserDisplayName(userID, displayName string) error {
	return s.setUserField(userID, AccountPatch{DisplayName: &displayName})
}

// SetUserEmail sets the login address.
func (s *Store) SetUserEmail(userID, email string) error {
	return s.setUserField(userID, AccountPatch{Email: &email})
}

// SetUserPreferredLanguage stores the account's language; an empty value clears it
// so the account inherits the platform default again.
func (s *Store) SetUserPreferredLanguage(userID, language string) error {
	return s.setUserField(userID, AccountPatch{PreferredLanguage: &language})
}

// setUserField applies one field change through the backend, which is where the
// "no such account" answer comes from.
func (s *Store) setUserField(userID string, patch AccountPatch) error {
	backend, err := s.accountBackend()
	if err != nil {
		return err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.Patch(ctx, userID, patch)
}

// GetUserPreferredLanguage returns the account's language ("" = inherit).
func (s *Store) GetUserPreferredLanguage(userID string) (string, error) {
	sum, err := s.GetUserAccount(userID)
	if err != nil || sum == nil {
		return "", err
	}
	return sum.PreferredLanguage, nil
}

// GetUserInfo returns the account's status and plan, which is what the runtime asks
// before it decides how to run a message.
func (s *Store) GetUserInfo(userID string) (status, level string, err error) {
	backend, err := s.accountBackend()
	if err != nil {
		return "", "", err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.Info(ctx, userID)
}

// ClaimBootstrapAdmin takes the one-shot administrator hand-out.
func (s *Store) ClaimBootstrapAdmin(userID string) (bool, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return false, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.ClaimBootstrapAdmin(ctx, userID)
}

// ReleaseBootstrapAdmin gives the hand-out back after a failed claim.
func (s *Store) ReleaseBootstrapAdmin(userID string) error {
	backend, err := s.accountBackend()
	if err != nil {
		return err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.ReleaseBootstrapAdmin(ctx, userID)
}

// SetUserPassword sets the credential and the "must change" flag.
func (s *Store) SetUserPassword(userID, passwordHash string, mustChange bool) error {
	return s.setPassword(userID, passwordHash, mustChange, false)
}

// SetUserPasswordKeepingFlag sets the credential and leaves the flag as it is,
// which is what a password the person chose themselves does.
func (s *Store) SetUserPasswordKeepingFlag(userID, passwordHash string) error {
	return s.setPassword(userID, passwordHash, false, true)
}

func (s *Store) setPassword(userID, passwordHash string, mustChange, keepFlag bool) error {
	backend, err := s.accountBackend()
	if err != nil {
		return err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.SetPassword(ctx, userID, passwordHash, mustChange, keepFlag)
}

// UserPasswordHash reads the stored credential ("" when the account has none).
func (s *Store) UserPasswordHash(userID string) (string, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return "", err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.PasswordHash(ctx, userID)
}

// UserExists reports whether the account exists.
func (s *Store) UserExists(userID string) (bool, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return false, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.Exists(ctx, userID)
}

// CountActiveAdmins is the number the first-registration rule and the console read.
func (s *Store) CountActiveAdmins() (int, error) {
	_, admins, _, err := s.counts()
	return admins, err
}

// CountActiveUsers is the number the console's overview shows.
func (s *Store) CountActiveUsers() (int, error) {
	active, _, _, err := s.counts()
	return active, err
}

// CountUsersByTier splits the accounts by plan (every account, deleted included,
// and the three plans always present).
func (s *Store) CountUsersByTier() (map[string]int, error) {
	_, _, tiers, err := s.counts()
	return tiers, err
}

func (s *Store) counts() (int, int, map[string]int, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return 0, 0, nil, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.Counts(ctx)
}

// GetUserAccount reads one account as the console shows it.
func (s *Store) GetUserAccount(userID string) (*UserSummary, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return nil, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	sum, err := backend.Account(ctx, userID)
	if err != nil {
		return nil, err
	}
	if sum == nil {
		return nil, ErrUserNotFound
	}
	return sum, nil
}

// FindUserByEmail reads one account by address.
func (s *Store) FindUserByEmail(email string) (*UserSummary, error) {
	if email == "" {
		return nil, ErrUserNotFound
	}
	backend, err := s.accountBackend()
	if err != nil {
		return nil, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	sum, err := backend.AccountByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if sum == nil {
		return nil, ErrUserNotFound
	}
	return sum, nil
}

// UserChannels lists the channel names linked to an account.
func (s *Store) UserChannels(userID string) ([]string, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return nil, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.Channels(ctx, userID)
}

// ListUserSummaries is the console's user page.
func (s *Store) ListUserSummaries(includeDeleted bool, limit int) ([]UserSummary, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return nil, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.ListAccounts(ctx, includeDeleted, limit)
}

// SearchUserSummaries is the console's search box.
func (s *Store) SearchUserSummaries(q string, limit int) ([]UserSummary, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return nil, err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.SearchAccounts(ctx, q, limit)
}

// ContainerEnqueuer is the provisioning queue, as the onboarding path needs it:
// ask for one (user, binding)'s container. The provisioner owns the queue.
type ContainerEnqueuer interface {
	Enabled() bool
	Enqueue(ctx context.Context, userID string, bindingID int64, agentID, level string) error
}

// SetContainerEnqueuer wires the provisioning queue. Without it onboarding creates
// accounts but asks for no container, which is what a deployment without a
// provisioner gets.
func (s *Store) SetContainerEnqueuer(e ContainerEnqueuer) { s.containers = e }

// AgentEnsurer is the agent rows' owner, as onboarding needs it: "make sure this
// account has an agent, and tell me which one".
type AgentEnsurer interface {
	Enabled() bool
	EnsureDefaultAgent(ctx context.Context, userID string) (string, error)
}

// SetAgentEnsurer wires the portal. Without it onboarding creates accounts with
// no agent, which is what the deployment had before this hook existed.
func (s *Store) SetAgentEnsurer(e AgentEnsurer) { s.agents = e }

// UserIDForBinding answers whose binding id this is.
func (s *Store) UserIDForBinding(bindingID int64) (string, error) {
	backend, err := s.accountBackend()
	if err != nil {
		return "", err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.BindingOwner(ctx, bindingID)
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
	backend, err := s.accountBackend()
	if err != nil {
		return err
	}
	ctx, cancel := accountContext()
	defer cancel()
	return backend.SetChannel(ctx, userID, channel, enabled)
}

// bindingAgentID is the agent a binding's container runs: the id the "binding-N"
// names quote, and the name the provisioning path asks for.
func bindingAgentID(bindingID int64) string {
	return "binding-" + strconv.FormatInt(bindingID, 10)
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
