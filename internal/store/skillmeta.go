package store

import (
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	userSecretsCollection   = "user_secrets"
	userSkillAcksCollection = "user_skill_acks"
)

// storedUserSecret is one secret value of one (user, capability).
//
// _id stays an ObjectID and the four fields that were the primary key become a
// unique index: the alternative — an id glued out of four strings — is the one
// separator typo away from merging two secrets.
type storedUserSecret struct {
	ID             bson.ObjectID `bson:"_id,omitempty"`
	UserID         string        `bson:"user_id"`
	CapabilityKind string        `bson:"capability_kind"`
	Ref            string        `bson:"ref"`
	SecretKey      string        `bson:"secret_key"`
	Value          string        `bson:"value"`
	UpdatedAt      time.Time     `bson:"updated_at"`
}

func secretIdentity(userID, kind, ref, key string) bson.D {
	return bson.D{
		{Key: "user_id", Value: userID},
		{Key: "capability_kind", Value: kind},
		{Key: "ref", Value: ref},
		{Key: "secret_key", Value: key},
	}
}

// UpsertUserSecret stores (or replaces) a user-supplied secret value.
func (s *Store) UpsertUserSecret(userID, kind, ref, key, value string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// The identity comes from the filter on upsert, so only the value is set —
	// `ON DUPLICATE KEY UPDATE value = VALUES(value)`.
	err := retryOnDuplicateUpsert(func() error {
		_, err := s.db.Collection(userSecretsCollection).UpdateOne(ctx,
			secretIdentity(userID, kind, ref, key),
			bson.D{{Key: "$set", Value: bson.D{
				{Key: "value", Value: value},
				{Key: "updated_at", Value: time.Now().UTC()},
			}}},
			options.UpdateOne().SetUpsert(true))
		return err
	})
	if err != nil {
		return fmt.Errorf("upsert user secret: %w", err)
	}
	return nil
}

// ListUserSecrets returns the stored secret keys for a capability. Values are
// intended for runtime use only and must never be returned to the browser.
func (s *Store) ListUserSecrets(userID, kind, ref string) (map[string]string, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(userSecretsCollection).Find(ctx, bson.D{
		{Key: "user_id", Value: userID},
		{Key: "capability_kind", Value: kind},
		{Key: "ref", Value: ref},
	}, options.Find().SetSort(bson.D{{Key: "secret_key", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list user secrets: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := map[string]string{}
	for cur.Next(ctx) {
		var doc struct {
			SecretKey string `bson:"secret_key"`
			Value     string `bson:"value"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode user secret: %w", err)
		}
		out[doc.SecretKey] = doc.Value
	}
	return out, cur.Err()
}

// DeleteUserSecret removes one secret value.
func (s *Store) DeleteUserSecret(userID, kind, ref, key string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if _, err := s.db.Collection(userSecretsCollection).DeleteOne(ctx,
		secretIdentity(userID, kind, ref, key)); err != nil {
		return fmt.Errorf("delete user secret: %w", err)
	}
	return nil
}

// storedSkillAck is one acknowledged (user, skill) version.
type storedSkillAck struct {
	ID           bson.ObjectID `bson:"_id,omitempty"`
	UserID       string        `bson:"user_id"`
	SkillID      string        `bson:"skill_id"`
	AckedVersion int           `bson:"acked_version"`
	UpdatedAt    time.Time     `bson:"updated_at"`
}

func skillAckIdentity(userID, skillID string) bson.D {
	return bson.D{
		{Key: "user_id", Value: userID},
		{Key: "skill_id", Value: skillID},
	}
}

// AckSkill records that a user acknowledged a platform skill version.
func (s *Store) AckSkill(userID, skillID string, version int) error {
	ctx, cancel := opCtx()
	defer cancel()

	// $max is the whole of `GREATEST(acked_version, VALUES(acked_version))`, and
	// it is atomic on its own: an acknowledgement that arrives out of order
	// cannot lower the recorded version.
	err := retryOnDuplicateUpsert(func() error {
		_, err := s.db.Collection(userSkillAcksCollection).UpdateOne(ctx,
			skillAckIdentity(userID, skillID),
			bson.D{
				{Key: "$max", Value: bson.D{{Key: "acked_version", Value: version}}},
				{Key: "$set", Value: bson.D{{Key: "updated_at", Value: time.Now().UTC()}}},
			},
			options.UpdateOne().SetUpsert(true))
		return err
	})
	if err != nil {
		return fmt.Errorf("ack skill: %w", err)
	}
	return nil
}

// SkillAckVersion returns the acknowledged version for (user, skill), or 0.
func (s *Store) SkillAckVersion(userID, skillID string) (int, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc struct {
		AckedVersion int `bson:"acked_version"`
	}
	err := s.db.Collection(userSkillAcksCollection).FindOne(ctx,
		skillAckIdentity(userID, skillID)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get skill ack: %w", err)
	}
	return doc.AckedVersion, nil
}

// skillAckMap returns all acknowledged versions for a user keyed by skill id.
func (s *Store) skillAckMap(userID string) (map[string]int, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(userSkillAcksCollection).Find(ctx,
		bson.D{{Key: "user_id", Value: userID}})
	if err != nil {
		return nil, fmt.Errorf("list skill acks: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := map[string]int{}
	for cur.Next(ctx) {
		var doc storedSkillAck
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode skill ack: %w", err)
		}
		out[doc.SkillID] = doc.AckedVersion
	}
	return out, cur.Err()
}

// GetUserSkillByName returns a user-scoped skill owned by userID, else ErrNotFound.
func (s *Store) GetUserSkillByName(userID, name string) (*SkillRecord, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedSkill
	err := s.db.Collection(skillsCollection).FindOne(ctx, bson.D{
		{Key: "scope", Value: "user"},
		{Key: "owner_user_id", Value: userID},
		{Key: "name", Value: name},
	}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, errCapNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get skill by name: %w", err)
	}
	rec := doc.record()
	return &rec, nil
}
