package store

import (
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// The three collections that used to be somebody else's tables.
//
// `oauth_providers` belonged to the auth service (and the console read it),
// `git_browser_shortcuts` to the console, `refresh_tokens` to the auth service.
// They live here now because the bus is the **only** service that talks to a
// database: everything else goes through its API. That is the whole point of the
// last step of the storage move — one owner per collection, one place with
// credentials, one place that runs migrations.
const (
	oauthProvidersCollection = "oauth_providers"
	gitShortcutsCollection   = "git_browser_shortcuts"
	refreshTokensCollection  = "refresh_tokens"
)

// OAuthLink is one directory/identity link: this user signs in as that provider
// identity (an LDAP DN, an OAuth subject). The source table's primary key was
// (provider, provider_id), which is what keeps "one identity, one account" true.
type OAuthLink struct {
	UserID     string `json:"user_id"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
}

// GitShortcut is a user's starred path inside one agent's container.
type GitShortcut struct {
	ID      int64  `json:"id"`
	UserID  string `json:"user_id"`
	AgentID string `json:"agent_id"`
	Path    string `json:"path"`
	Label   string `json:"label,omitempty"`
}

// storedGitShortcut is the document shape. created_at is a date rather than the
// source column's timestamp text, and the list is ordered by it.
type storedGitShortcut struct {
	ID        int64     `bson:"_id"`
	UserID    string    `bson:"user_id"`
	AgentID   string    `bson:"agent_id"`
	Path      string    `bson:"path"`
	Label     string    `bson:"label,omitempty"`
	CreatedAt time.Time `bson:"created_at"`
}

func (d storedGitShortcut) shortcut() GitShortcut {
	return GitShortcut{ID: d.ID, UserID: d.UserID, AgentID: d.AgentID, Path: d.Path, Label: d.Label}
}

// storedOAuthLink is the document shape. The link is identified by the provider
// pair, so that pair carries the unique index and the upsert filter matches on
// it — the same guarantee the source table's composite primary key gave.
type storedOAuthLink struct {
	UserID     string `bson:"user_id"`
	Provider   string `bson:"provider"`
	ProviderID string `bson:"provider_id"`
}

// storedRefreshToken is the document shape: the token hash is the identity, as
// it was the source table's primary key, so it is the `_id` — one lookup, no
// second index, and a duplicate hash cannot exist.
type storedRefreshToken struct {
	TokenHash string    `bson:"_id"`
	UserID    string    `bson:"user_id"`
	ExpiresAt time.Time `bson:"expires_at"`
	CreatedAt time.Time `bson:"created_at"`
}

// ======== oauth links ========

// LinkOAuth records that a user signs in as one provider identity.
//
// It upserts on (provider, provider_id) rather than inserting and hoping: the
// source table's primary key is that pair, so re-linking an identity is an update
// of who owns it, not a second row. (The SQL INSERT would have failed with a
// duplicate key there; the callers checked first.) Uniqueness is enforced by the
// index in migrations-mongo/013, not by this code path.
func (s *Store) LinkOAuth(userID, provider, providerID string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if userID == "" || provider == "" || providerID == "" {
		return fmt.Errorf("link oauth: user_id, provider and provider_id are required")
	}
	if _, err := s.db.Collection(oauthProvidersCollection).UpdateOne(ctx,
		bson.D{{Key: "provider", Value: provider}, {Key: "provider_id", Value: providerID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "user_id", Value: userID},
			{Key: "provider", Value: provider},
			{Key: "provider_id", Value: providerID},
		}}},
		options.UpdateOne().SetUpsert(true),
	); err != nil {
		return fmt.Errorf("link oauth %s/%s: %w", provider, providerID, err)
	}
	return nil
}

// OAuthLinks returns every identity link a user has, ordered by provider then
// identity so the answer is stable.
func (s *Store) OAuthLinks(userID string) ([]OAuthLink, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(oauthProvidersCollection).Find(ctx,
		bson.D{{Key: "user_id", Value: userID}},
		options.Find().SetSort(bson.D{{Key: "provider", Value: 1}, {Key: "provider_id", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list oauth links: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []OAuthLink{}
	for cur.Next(ctx) {
		var doc storedOAuthLink
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode oauth link: %w", err)
		}
		out = append(out, OAuthLink{UserID: doc.UserID, Provider: doc.Provider, ProviderID: doc.ProviderID})
	}
	return out, cur.Err()
}

// UnlinkOAuth drops a user's link for one provider.
func (s *Store) UnlinkOAuth(userID, provider string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if _, err := s.db.Collection(oauthProvidersCollection).DeleteMany(ctx,
		bson.D{{Key: "user_id", Value: userID}, {Key: "provider", Value: provider}},
	); err != nil {
		return fmt.Errorf("unlink oauth %s: %w", provider, err)
	}
	return nil
}

// OAuthProviderLinks returns every link for one provider, so a caller can answer
// "which accounts sign in through the directory" without a second round trip per
// user. The console's user list reads exactly this.
func (s *Store) OAuthProviderLinks(provider string) ([]OAuthLink, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(oauthProvidersCollection).Find(ctx,
		bson.D{{Key: "provider", Value: provider}},
		options.Find().SetSort(bson.D{{Key: "user_id", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("list oauth links for %s: %w", provider, err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []OAuthLink{}
	for cur.Next(ctx) {
		var doc storedOAuthLink
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode oauth link: %w", err)
		}
		out = append(out, OAuthLink{UserID: doc.UserID, Provider: doc.Provider, ProviderID: doc.ProviderID})
	}
	return out, cur.Err()
}

// OAuthLinkOwner returns the account one provider identity belongs to.
//
// It is the sign-in flow's second question: the first is "who is this
// directory subject", the second is "what does that account look like". The
// identity pair is unique (the source table's primary key), so the answer is
// either one account or none.
func (s *Store) OAuthLinkOwner(provider, providerID string) (string, bool, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedOAuthLink
	err := s.db.Collection(oauthProvidersCollection).FindOne(ctx,
		bson.D{{Key: "provider", Value: provider}, {Key: "provider_id", Value: providerID}},
		options.FindOne().SetProjection(bson.D{{Key: "user_id", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("look up oauth identity %s/%s: %w", provider, providerID, err)
	}
	return doc.UserID, true, nil
}

// ======== git browser shortcuts ========

// GitShortcuts lists a user's starred paths, newest first, optionally narrowed to
// one agent. The ordering matches the SQL it replaces (created_at DESC, id DESC),
// with the id as the tie-break because two stars can land in the same
// millisecond.
func (s *Store) GitShortcuts(userID, agentID string) ([]GitShortcut, error) {
	ctx, cancel := opCtx()
	defer cancel()

	filter := bson.D{{Key: "user_id", Value: userID}}
	if agentID != "" {
		filter = append(filter, bson.E{Key: "agent_id", Value: agentID})
	}
	cur, err := s.db.Collection(gitShortcutsCollection).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "_id", Value: -1}}))
	if err != nil {
		return nil, fmt.Errorf("list git shortcuts: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []GitShortcut{}
	for cur.Next(ctx) {
		var doc storedGitShortcut
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode git shortcut: %w", err)
		}
		out = append(out, doc.shortcut())
	}
	return out, cur.Err()
}

// SaveGitShortcut stars a path, or re-labels it when it is already starred. It
// returns the row either way, with the id it already had — the SQL did the same
// find-then-update-or-insert, and the unique index on (user_id, agent_id, path)
// makes the race safe: a concurrent insert loses with a duplicate-key error and
// is retried as the update it should have been.
func (s *Store) SaveGitShortcut(userID, agentID, path, label string) (GitShortcut, error) {
	ctx, cancel := opCtx()
	defer cancel()

	if userID == "" || agentID == "" || path == "" {
		return GitShortcut{}, fmt.Errorf("save git shortcut: user_id, agent_id and path are required")
	}
	identity := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "agent_id", Value: agentID},
		{Key: "path", Value: path},
	}

	var existing storedGitShortcut
	err := s.db.Collection(gitShortcutsCollection).FindOne(ctx, identity).Decode(&existing)
	switch {
	case err == nil:
		if _, err := s.db.Collection(gitShortcutsCollection).UpdateOne(ctx, identity,
			bson.D{{Key: "$set", Value: bson.D{{Key: "label", Value: label}}}}); err != nil {
			return GitShortcut{}, fmt.Errorf("relabel git shortcut: %w", err)
		}
		existing.Label = label
		return existing.shortcut(), nil
	case !errors.Is(err, mongo.ErrNoDocuments):
		return GitShortcut{}, fmt.Errorf("find git shortcut: %w", err)
	}

	id, err := s.nextSequence(ctx, gitShortcutsCollection)
	if err != nil {
		return GitShortcut{}, err
	}
	doc := storedGitShortcut{
		ID: id, UserID: userID, AgentID: agentID, Path: path, Label: label,
		CreatedAt: time.Now().UTC(),
	}
	if _, err := s.db.Collection(gitShortcutsCollection).InsertOne(ctx, doc); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Another star of the same path won the race: it is an update now.
			return s.SaveGitShortcut(userID, agentID, path, label)
		}
		return GitShortcut{}, fmt.Errorf("insert git shortcut: %w", err)
	}
	return doc.shortcut(), nil
}

// DeleteGitShortcut removes one of the caller's stars. Deleting nothing is an
// error, as it was in SQL: the console wants to say "not found" rather than
// pretend it deleted something.
func (s *Store) DeleteGitShortcut(userID string, id int64) error {
	ctx, cancel := opCtx()
	defer cancel()

	res, err := s.db.Collection(gitShortcutsCollection).DeleteOne(ctx,
		bson.D{{Key: "_id", Value: id}, {Key: "user_id", Value: userID}})
	if err != nil {
		return fmt.Errorf("delete git shortcut: %w", err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("shortcut %d not found", id)
	}
	return nil
}

// ======== refresh tokens ========

// SaveRefreshToken stores a token hash. The hash is the identity, so re-saving
// one replaces it rather than adding a second row — the same thing the source
// table's primary key forced.
func (s *Store) SaveRefreshToken(tokenHash, userID string, expiresAt time.Time) error {
	ctx, cancel := opCtx()
	defer cancel()

	if tokenHash == "" || userID == "" {
		return fmt.Errorf("save refresh token: token_hash and user_id are required")
	}
	if _, err := s.db.Collection(refreshTokensCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: tokenHash}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "user_id", Value: userID},
			{Key: "expires_at", Value: expiresAt.UTC()},
		}}, {Key: "$setOnInsert", Value: bson.D{
			{Key: "created_at", Value: time.Now().UTC()},
		}}},
		options.UpdateOne().SetUpsert(true),
	); err != nil {
		return fmt.Errorf("save refresh token: %w", err)
	}
	return nil
}

// RefreshToken returns the owner and expiry of a token hash, or nil when the
// hash is unknown. The caller decides what to do about an expired one.
func (s *Store) RefreshToken(tokenHash string) (userID string, expiresAt time.Time, found bool, err error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedRefreshToken
	err = s.db.Collection(refreshTokensCollection).FindOne(ctx,
		bson.D{{Key: "_id", Value: tokenHash}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", time.Time{}, false, nil
	}
	if err != nil {
		return "", time.Time{}, false, fmt.Errorf("read refresh token: %w", err)
	}
	return doc.UserID, doc.ExpiresAt, true, nil
}

// DeleteRefreshToken revokes one token. Deleting an unknown hash is not an error:
// logout must be safe to repeat.
func (s *Store) DeleteRefreshToken(tokenHash string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if _, err := s.db.Collection(refreshTokensCollection).DeleteOne(ctx,
		bson.D{{Key: "_id", Value: tokenHash}}); err != nil {
		return fmt.Errorf("delete refresh token: %w", err)
	}
	return nil
}

// DeleteUserRefreshTokens revokes every token a user holds (a password change or
// an account suspension has to end the sessions, not just the next login).
func (s *Store) DeleteUserRefreshTokens(userID string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if _, err := s.db.Collection(refreshTokensCollection).DeleteMany(ctx,
		bson.D{{Key: "user_id", Value: userID}}); err != nil {
		return fmt.Errorf("delete refresh tokens for %s: %w", userID, err)
	}
	return nil
}

// PurgeExpiredRefreshTokens removes tokens whose expiry has passed. The store
// does not do this on a timer (the bus has no scheduler for housekeeping): the
// auth service calls it on the paths that already look at tokens, which is where
// an expired row costs something.
func (s *Store) PurgeExpiredRefreshTokens(now time.Time) (int64, error) {
	ctx, cancel := opCtx()
	defer cancel()

	res, err := s.db.Collection(refreshTokensCollection).DeleteMany(ctx,
		bson.D{{Key: "expires_at", Value: bson.D{{Key: "$lt", Value: now.UTC()}}}})
	if err != nil {
		return 0, fmt.Errorf("purge refresh tokens: %w", err)
	}
	return res.DeletedCount, nil
}
