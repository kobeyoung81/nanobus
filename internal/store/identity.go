package store

import (
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// The directory links: which provider identity signs in as which account.
//
// The starred git paths and the session tickets have moved to the auth service
// with the rest of identity; the links follow when the account surface does.
const (
	oauthProvidersCollection = "auth_oauth_providers"
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
