package stream

// Platform events are the change notifications agents and the console subscribe
// to: a configuration change, a capability refresh, an agent record changing.
// They used to live only in one process's memory, so a subscriber connected to
// another replica never saw them. They are written here now, with the same
// cursor discipline as the conversation log.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// PlatformEventsCollection holds the change notifications.
const PlatformEventsCollection = "bus_platform_events"

const (
	// PlatformEventTTL bounds replay: a subscriber that reconnects inside the
	// window catches up; beyond it, notifications are re-derivable anyway (each
	// one only tells the receiver to re-read something). It is a day, not ten
	// minutes: these events are rare, and a client that was off overnight should
	// still learn that the catalogue changed.
	PlatformEventTTL = 24 * time.Hour
	// KeepPlatformEvents bounds the collection's size regardless of TTL.
	KeepPlatformEvents = 10000
	platformTrimEvery  = 500
)

// PlatformEvent is one stored notification.
type PlatformEvent struct {
	ID      bson.ObjectID `bson:"_id,omitempty"`
	At      time.Time     `bson:"at"`
	Type    string        `bson:"type"`
	Content string        `bson:"content"`
}

// PlatformStore writes and reads the platform event log.
type PlatformStore struct {
	coll    *mongo.Collection
	mu      sync.Mutex
	appends int
}

// NewPlatformStore wraps the platform database.
func NewPlatformStore(db *mongo.Database) *PlatformStore {
	return &PlatformStore{coll: db.Collection(PlatformEventsCollection)}
}

// Append stores one notification and returns its id.
func (s *PlatformStore) Append(ctx context.Context, eventType, content string) (string, error) {
	res, err := s.coll.InsertOne(ctx, PlatformEvent{At: time.Now().UTC(), Type: eventType, Content: content})
	if err != nil {
		return "", fmt.Errorf("insert platform event: %w", err)
	}
	id, _ := res.InsertedID.(bson.ObjectID)

	s.mu.Lock()
	s.appends++
	trim := s.appends >= platformTrimEvery
	if trim {
		s.appends = 0
	}
	s.mu.Unlock()
	if trim {
		if err := s.Trim(ctx, KeepPlatformEvents); err != nil {
			return id.Hex(), nil // housekeeping, never the event's problem
		}
	}
	return id.Hex(), nil
}

// Since returns notifications after afterID, oldest first.
func (s *PlatformStore) Since(ctx context.Context, afterID string, limit int) ([]PlatformEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	filter := bson.D{}
	if afterID != "" {
		oid, err := bson.ObjectIDFromHex(afterID)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor")
		}
		filter = append(filter, bson.E{Key: "_id", Value: bson.D{{Key: "$gt", Value: oid}}})
	}
	cursor, err := s.coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("read platform events: %w", err)
	}
	events := []PlatformEvent{}
	if err := cursor.All(ctx, &events); err != nil {
		return nil, fmt.Errorf("decode platform events: %w", err)
	}
	return events, nil
}

// Newest returns the newest event id, or "" when the log is empty.
func (s *PlatformStore) Newest(ctx context.Context) (string, error) {
	var doc struct {
		ID bson.ObjectID `bson:"_id"`
	}
	err := s.coll.FindOne(ctx, bson.D{},
		options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}}).SetProjection(bson.D{{Key: "_id", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read newest platform event: %w", err)
	}
	return doc.ID.Hex(), nil
}

// Trim keeps the newest keep events.
func (s *PlatformStore) Trim(ctx context.Context, keep int) error {
	if keep <= 0 {
		keep = KeepPlatformEvents
	}
	var boundary struct {
		ID bson.ObjectID `bson:"_id"`
	}
	err := s.coll.FindOne(ctx, bson.D{},
		options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}}).SetSkip(int64(keep)).SetProjection(bson.D{{Key: "_id", Value: 1}}),
	).Decode(&boundary)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read platform trim boundary: %w", err)
	}
	_, err = s.coll.DeleteMany(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$lte", Value: boundary.ID}}}})
	if err != nil {
		return fmt.Errorf("trim platform events: %w", err)
	}
	return nil
}
