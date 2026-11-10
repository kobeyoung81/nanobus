package store

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// countersCollection holds the named counters that stand in for MySQL's
// AUTO_INCREMENT.
const countersCollection = "sys_counters"

// nextSequence returns the next value of a named counter.
//
// Two migrated collections expose an integer id — audit_log and alert_rules —
// and both ids are part of an API (the admin UI deletes an alert rule by id), so
// they cannot become ObjectIds. MySQL handed them out from the table's own
// auto-increment sequence; here one document per sequence holds it, bumped by a
// single atomic findOneAndUpdate. That is the same shape of operation the rest
// of this migration is built on, and it is available on a standalone server —
// unlike the transaction a "read, add one, write" pair would need.
func (s *Store) nextSequence(ctx context.Context, name string) (int64, error) {
	var doc struct {
		Seq int64 `bson:"seq"`
	}
	// Upserting a counter can race another upsert and raise E11000; the
	// documented remedy is one retry, and the retry is safe because the failed
	// attempt did not apply its $inc.
	err := retryOnDuplicateUpsert(func() error {
		return s.db.Collection(countersCollection).FindOneAndUpdate(ctx,
			bson.D{{Key: "_id", Value: name}},
			bson.D{{Key: "$inc", Value: bson.D{{Key: "seq", Value: int64(1)}}}},
			options.FindOneAndUpdate().
				SetUpsert(true).
				SetReturnDocument(options.After),
		).Decode(&doc)
	})
	if err != nil {
		return 0, fmt.Errorf("next %s id: %w", name, err)
	}
	return doc.Seq, nil
}

// resyncSequence moves a counter up to the highest numeric _id the collection
// already holds, so it never hands out an id that exists.
//
// P1 renamed collections (`audit_log` became `admin_audit_log`) but the counter
// is keyed by name, so the new name started again at 1 while the migrated rows
// kept their ids — every insert then collided and, because the audit write was
// best-effort, the failure was invisible for days. This is the repair path: it
// is idempotent and only ever moves the counter forward.
func (s *Store) resyncSequence(ctx context.Context, counter, collection string) error {
	var doc struct {
		ID int64 `bson:"_id"`
	}
	err := s.db.Collection(collection).FindOne(ctx, bson.D{},
		options.FindOne().
			SetSort(bson.D{{Key: "_id", Value: -1}}).
			SetProjection(bson.D{{Key: "_id", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		// Nothing stored under this name yet: the counter is already right.
		return nil
	}
	if err != nil {
		return fmt.Errorf("read highest %s id: %w", collection, err)
	}
	if _, err := s.db.Collection(countersCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: counter}},
		bson.D{{Key: "$max", Value: bson.D{{Key: "seq", Value: doc.ID}}}},
		options.UpdateOne().SetUpsert(true),
	); err != nil {
		return fmt.Errorf("resync %s counter: %w", counter, err)
	}
	return nil
}
