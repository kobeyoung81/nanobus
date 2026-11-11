package store

import (
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	userSkillAcksCollection = "admin_user_skill_acks"
)

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
