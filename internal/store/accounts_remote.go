package store

// The account surface, forwarded.
//
// The account document and its links are the auth service's; the bus still serves
// the endpoints the console and the runtime call. Those handlers keep the store
// methods they always called, and the methods now ask the module that owns the
// rows. One place per method, so the handlers did not have to change.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
)

// AccountPatch names the account fields a request may change. A nil field was not
// mentioned, which is not the same as "set it to empty".
type AccountPatch struct {
	Status            *string
	Role              *string
	Tier              *string
	DisplayName       *string
	Email             *string
	PreferredLanguage *string
}

// AccountBackend is the account domain's operations. The bus holds one of these
// (RemoteAccounts in production, a fake in tests) and never a database handle for
// this domain.
type AccountBackend interface {
	Resolve(ctx context.Context, channel, senderID string) (ResolvedBinding, error)
	Register(ctx context.Context, userID, displayName string) error
	LinkChannel(ctx context.Context, userID, channel, senderID string) error
	Account(ctx context.Context, userID string) (*UserSummary, error)
	AccountByEmail(ctx context.Context, email string) (*UserSummary, error)
	ListAccounts(ctx context.Context, includeDeleted bool, limit int) ([]UserSummary, error)
	SearchAccounts(ctx context.Context, q string, limit int) ([]UserSummary, error)
	AllUserIDs(ctx context.Context) ([]string, error)
	Patch(ctx context.Context, userID string, patch AccountPatch) error
	PasswordHash(ctx context.Context, userID string) (string, error)
	SetPassword(ctx context.Context, userID, hash string, mustChange, keepFlag bool) error
	ClaimBootstrapAdmin(ctx context.Context, userID string) (bool, error)
	ReleaseBootstrapAdmin(ctx context.Context, userID string) error
	Channels(ctx context.Context, userID string) ([]string, error)
	SetChannel(ctx context.Context, userID, channel string, enabled bool) error
	Info(ctx context.Context, userID string) (status, tier string, err error)
	Counts(ctx context.Context) (active, admins int, tiers map[string]int, err error)
	Exists(ctx context.Context, userID string) (bool, error)
	BindingOwner(ctx context.Context, bindingID int64) (string, error)
}

// SetAccountBackend wires the account domain's owner. Without it every account call
// reports that the module is not configured rather than answering empty.
func (s *Store) SetAccountBackend(b AccountBackend) { s.accounts = b }

// AccountBackend returns the wired backend (nil when none). It exists for the test
// helpers: a seeded account has to land where the module that owns the rows keeps
// it, and the fake stands in for that module.
func (s *Store) AccountBackend() AccountBackend { return s.accounts }

// accountBackend returns the backend or an error that says what is missing.
func (s *Store) accountBackend() (AccountBackend, error) {
	if s.accounts == nil {
		return nil, fmt.Errorf("the auth module is not configured")
	}
	return s.accounts, nil
}

// accountOp bounds one call to the auth module. The account paths sit in front of
// sign-in and onboarding, so a hung module must surface as an error, not a hang.
const accountOp = 5 * time.Second

func accountContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), accountOp)
}

// RemoteAccounts is the backend that asks nanoauth's internal surface.
type RemoteAccounts struct {
	client *authclient.Client
}

// NewRemoteAccounts wraps a client.
func NewRemoteAccounts(client *authclient.Client) *RemoteAccounts {
	return &RemoteAccounts{client: client}
}

func (r *RemoteAccounts) Resolve(ctx context.Context, channel, senderID string) (ResolvedBinding, error) {
	resolved, err := r.client.ResolveAccount(ctx, channel, senderID)
	if errors.Is(err, authclient.ErrChannelDisabled) {
		return ResolvedBinding{}, ErrChannelDisabled
	}
	if err != nil {
		return ResolvedBinding{}, err
	}
	return ResolvedBinding{UserID: resolved.UserID, BindingID: resolved.BindingID, IsNew: resolved.IsNew}, nil
}

func (r *RemoteAccounts) Register(ctx context.Context, userID, displayName string) error {
	err := r.client.RegisterAccount(ctx, userID, displayName)
	// A taken address comes back as the module's own conflict.
	var statusErr *authclient.StatusError
	if errors.As(err, &statusErr) && statusErr.Status == http.StatusConflict {
		return ErrEmailTaken
	}
	return err
}

func (r *RemoteAccounts) LinkChannel(ctx context.Context, userID, channel, senderID string) error {
	return r.client.LinkChannel(ctx, userID, channel, senderID)
}

func (r *RemoteAccounts) Account(ctx context.Context, userID string) (*UserSummary, error) {
	account, err := r.client.Account(ctx, userID)
	if errors.Is(err, authclient.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return remoteSummary(account), nil
}

// remoteSummary maps the module's answer onto the store's shape.
func remoteSummary(a *authclient.Account) *UserSummary {
	if a == nil {
		return nil
	}
	return &UserSummary{
		ID: a.UserID, DisplayName: a.DisplayName, Email: a.Email,
		Role: a.Role, Status: a.Status, Tier: a.Tier,
		MustChangePassword: a.MustChangePassword,
		PreferredLanguage:  a.PreferredLanguage,
	}
}

func remoteSummaries(accounts []authclient.Account) []UserSummary {
	out := make([]UserSummary, 0, len(accounts))
	for i := range accounts {
		out = append(out, *remoteSummary(&accounts[i]))
	}
	return out
}

func (r *RemoteAccounts) AccountByEmail(ctx context.Context, email string) (*UserSummary, error) {
	account, err := r.client.AccountByEmail(ctx, email)
	if errors.Is(err, authclient.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return remoteSummary(account), nil
}

func (r *RemoteAccounts) ListAccounts(ctx context.Context, includeDeleted bool, limit int) ([]UserSummary, error) {
	accounts, err := r.client.ListAccounts(ctx, includeDeleted, limit)
	if err != nil {
		return nil, err
	}
	return remoteSummaries(accounts), nil
}

func (r *RemoteAccounts) SearchAccounts(ctx context.Context, q string, limit int) ([]UserSummary, error) {
	accounts, err := r.client.SearchAccounts(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	return remoteSummaries(accounts), nil
}

func (r *RemoteAccounts) AllUserIDs(ctx context.Context) ([]string, error) {
	return r.client.AllUserIDs(ctx)
}

func (r *RemoteAccounts) Patch(ctx context.Context, userID string, patch AccountPatch) error {
	err := r.client.PatchAccount(ctx, userID, authclient.AccountPatch{
		Status: patch.Status, Role: patch.Role, Tier: patch.Tier,
		DisplayName: patch.DisplayName, Email: patch.Email,
		PreferredLanguage: patch.PreferredLanguage,
	})
	if errors.Is(err, authclient.ErrNotFound) {
		return ErrUserNotFound
	}
	return err
}

func (r *RemoteAccounts) PasswordHash(ctx context.Context, userID string) (string, error) {
	hash, err := r.client.PasswordHash(ctx, userID)
	if errors.Is(err, authclient.ErrNotFound) {
		return "", ErrUserNotFound
	}
	return hash, err
}

func (r *RemoteAccounts) SetPassword(ctx context.Context, userID, hash string, mustChange, keepFlag bool) error {
	err := r.client.SetPassword(ctx, userID, hash, mustChange, keepFlag)
	if errors.Is(err, authclient.ErrNotFound) {
		return ErrUserNotFound
	}
	return err
}

func (r *RemoteAccounts) ClaimBootstrapAdmin(ctx context.Context, userID string) (bool, error) {
	return r.client.ClaimBootstrapAdmin(ctx, userID)
}

func (r *RemoteAccounts) ReleaseBootstrapAdmin(ctx context.Context, userID string) error {
	return r.client.ReleaseBootstrapAdmin(ctx, userID)
}

func (r *RemoteAccounts) Channels(ctx context.Context, userID string) ([]string, error) {
	channels, err := r.client.Channels(ctx, userID)
	if errors.Is(err, authclient.ErrNotFound) {
		return nil, ErrUserNotFound
	}
	return channels, err
}

func (r *RemoteAccounts) SetChannel(ctx context.Context, userID, channel string, enabled bool) error {
	err := r.client.SetChannel(ctx, userID, channel, enabled)
	if errors.Is(err, authclient.ErrNotFound) {
		return ErrUserNotFound
	}
	return err
}

func (r *RemoteAccounts) Info(ctx context.Context, userID string) (string, string, error) {
	status, tier, err := r.client.AccountInfo(ctx, userID)
	if errors.Is(err, authclient.ErrNotFound) {
		return "", "", ErrUserNotFound
	}
	return status, tier, err
}

func (r *RemoteAccounts) Counts(ctx context.Context) (int, int, map[string]int, error) {
	return r.client.AccountCounts(ctx)
}

func (r *RemoteAccounts) Exists(ctx context.Context, userID string) (bool, error) {
	return r.client.UserExists(ctx, userID)
}

func (r *RemoteAccounts) BindingOwner(ctx context.Context, bindingID int64) (string, error) {
	userID, err := r.client.BindingOwner(ctx, bindingID)
	if errors.Is(err, authclient.ErrNotFound) {
		return "", ErrUserNotFound
	}
	return userID, err
}
