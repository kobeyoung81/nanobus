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
	oauthProvidersCollection = "auth_oauth_providers"
	refreshTokensCollection  = "auth_refresh_tokens"
)

// OAuthLink is one directory/identity link: this user signs in as that provider
// identity (an LDAP DN, an OAuth subject). The source table's primary key was
// (provider, provider_id), which is what keeps "one identity, one account" true.
type OAuthLink struct {
	UserID     string `json:"user_id"`
	Provider   string `json:"provider"`
	ProviderID string `json:"provider_id"`
}

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
