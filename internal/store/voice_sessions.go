package store

// The one-time ids that open the voice WebSocket live in the database so any
// bus replica can accept the socket the portal opened against another one. The
// id is the capability: only the portal can mint one, and each works once.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// VoiceSessionsCollection holds the ids until they expire or are claimed.
const VoiceSessionsCollection = "bus_voice_sessions"

// VoiceSessionStore mints and consumes one-time voice ids.
type VoiceSessionStore struct {
	coll *mongo.Collection
}

// NewVoiceSessionStore wraps the platform database.
func NewVoiceSessionStore(db *mongo.Database) *VoiceSessionStore {
	return &VoiceSessionStore{coll: db.Collection(VoiceSessionsCollection)}
}

// Mint issues an id that expires after ttl.
func (s *VoiceSessionStore) Mint(ctx context.Context, userID string, ttl time.Duration) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)
	_, err := s.coll.InsertOne(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "user_id", Value: userID},
		{Key: "expires_at", Value: time.Now().UTC().Add(ttl)},
	})
	if err != nil {
		return "", fmt.Errorf("mint voice session: %w", err)
	}
	return id, nil
}

// Claim consumes an id, returning the user it was minted for. A missing,
// expired or already-used id is refused; the delete is the claim, so two
// sockets racing for one id cannot both win.
func (s *VoiceSessionStore) Claim(ctx context.Context, id string) (string, bool, error) {
	var doc struct {
		UserID string `bson:"user_id"`
	}
	err := s.coll.FindOneAndDelete(ctx, bson.D{
		{Key: "_id", Value: id},
		{Key: "expires_at", Value: bson.D{{Key: "$gt", Value: time.Now().UTC()}}},
	}).Decode(&doc)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			return "", false, nil
		}
		return "", false, fmt.Errorf("claim voice session: %w", err)
	}
	return doc.UserID, true, nil
}
