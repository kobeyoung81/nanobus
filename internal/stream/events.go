// Package stream keeps the outbound chunk buffer. Delivery used to live only in
// this process's memory: a second bus replica could not serve a gateway that
// reconnected to it, and a conversation's events shared one channel-wide ring.
// The events are now written to bus_stream_events, keyed by conversation, and
// the in-memory ring remains only for older gateways that subscribe per channel.
package stream

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

// EventsCollection is the store of streamed chunks and completions.
const EventsCollection = "bus_stream_events"

const (
	// EventTTL is how long an event stays replayable. A reconnect inside the
	// window is served from the database; beyond it the client is told to reset.
	EventTTL = 10 * time.Minute
	// KeepPerConversation bounds one conversation's history regardless of TTL.
	KeepPerConversation = 1000
	// trimEvery trims once per this many appends of one conversation. Trimming
	// on every append would double the write cost for no benefit.
	trimEvery = 200
)

// ErrCursorExpired means the client's resume point is older than anything kept;
// the caller must restart from the oldest retained event and say so.
var ErrCursorExpired = errors.New("stream cursor is older than the retained window")

// Event is one stored chunk or completion.
type Event struct {
	ID        bson.ObjectID `bson:"_id,omitempty"`
	Channel   string        `bson:"channel"`
	ChatID    string        `bson:"chat_id"`
	At        time.Time     `bson:"at"`
	Type      string        `bson:"type"`
	Kind      string        `bson:"kind,omitempty"`
	MessageID string        `bson:"message_id,omitempty"`
	Seq       int           `bson:"seq,omitempty"`
	Content   string        `bson:"content,omitempty"`
	// FullContent is the assembled message on a "complete" event.
	FullContent string `bson:"full_content,omitempty"`
	Summary     string `bson:"summary,omitempty"`
	Error       string `bson:"error,omitempty"`
	ErrorDetail string `bson:"error_detail,omitempty"`
	AgentID     string `bson:"agent_id,omitempty"`
	UserID      string `bson:"user_id,omitempty"`
	SessionID   string `bson:"session_id,omitempty"`

	appends int
}

// ParseCursor validates a Last-Event-ID issued by this log.
func ParseCursor(id string) (bson.ObjectID, error) {
	return bson.ObjectIDFromHex(id)
}

// ToChunkEvent renders a stored event in the wire shape the gateway already
// parses, so switching from the in-memory ring to the log needs no client change.
func (e Event) ToChunkEvent() ChunkEvent {
	return ChunkEvent{
		Type:        e.Type,
		Kind:        e.Kind,
		MessageID:   e.MessageID,
		Seq:         e.Seq,
		Content:     e.Content,
		FullContent: e.FullContent,
		Summary:     e.Summary,
		Error:       e.Error,
		ErrorDetail: e.ErrorDetail,
		AgentID:     e.AgentID,
		UserID:      e.UserID,
		Channel:     e.Channel,
		ChatID:      e.ChatID,
		SessionID:   e.SessionID,
	}
}

// Store writes and reads the event stream.
type Store struct {
	coll *mongo.Collection
	mu   sync.Mutex
	// trims counts appends per conversation so trimming stays amortised.
	trims map[string]int
}

// NewStore wraps the platform database.
func NewStore(db *mongo.Database) *Store {
	return &Store{coll: db.Collection(EventsCollection), trims: make(map[string]int)}
}

func conversationKey(channel, chatID string) string { return channel + "\x00" + chatID }

// Append stores one event and returns its id (hex, as the SSE Last-Event-ID).
func (s *Store) Append(ctx context.Context, ev Event) (string, error) {
	if ev.ChatID == "" {
		return "", fmt.Errorf("chat_id is required")
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	res, err := s.coll.InsertOne(ctx, ev)
	if err != nil {
		return "", fmt.Errorf("insert stream event: %w", err)
	}
	id, _ := res.InsertedID.(bson.ObjectID)
	s.noteAppend(ctx, ev)
	return id.Hex(), nil
}

// AppendMany stores a batch in one round trip. Order inside the batch is the
// insertion order, so a chunk written together with its completion keeps that
// order in the log as well.
func (s *Store) AppendMany(ctx context.Context, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	docs := make([]any, len(evs))
	for i := range evs {
		if evs[i].ChatID == "" {
			return fmt.Errorf("chat_id is required")
		}
		if evs[i].At.IsZero() {
			evs[i].At = now
		}
		docs[i] = evs[i]
	}
	if _, err := s.coll.InsertMany(ctx, docs); err != nil {
		return fmt.Errorf("insert stream events: %w", err)
	}
	for _, ev := range evs {
		s.noteAppend(ctx, ev)
	}
	return nil
}

// noteAppend keeps the per-conversation trim counter and trims when it is due.
// Trimming is housekeeping: a failure must not fail the event.
func (s *Store) noteAppend(ctx context.Context, ev Event) {
	key := conversationKey(ev.Channel, ev.ChatID)
	s.mu.Lock()
	s.trims[key]++
	trim := s.trims[key] >= trimEvery
	if trim {
		s.trims[key] = 0
	}
	s.mu.Unlock()
	if trim {
		_ = s.Trim(ctx, ev.Channel, ev.ChatID, KeepPerConversation)
	}
}

// scanAll returns events after afterID across every conversation, oldest first.
// The tailer uses it as the safety net behind the change stream: one query per
// replica regardless of how many subscribers are connected.
func (s *Store) scanAll(ctx context.Context, afterID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	filter := bson.D{}
	if afterID != "" {
		oid, err := bson.ObjectIDFromHex(afterID)
		if err != nil {
			return nil, nil
		}
		filter = bson.D{{Key: "_id", Value: bson.D{{Key: "$gt", Value: oid}}}}
	}
	cursor, err := s.coll.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("scan stream events: %w", err)
	}
	events := []Event{}
	if err := cursor.All(ctx, &events); err != nil {
		return nil, fmt.Errorf("decode stream events: %w", err)
	}
	return events, nil
}

// newestEvent is the newest id in the whole collection ("" when empty). A
// freshly started tailer begins here: everything older is served by a
// subscriber's own replay, and re-publishing ten minutes of history to nobody
// would only cost queries.
func (s *Store) newestEvent(ctx context.Context) (string, error) {
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
		return "", fmt.Errorf("read newest stream event: %w", err)
	}
	return doc.ID.Hex(), nil
}

// Since returns the events after afterID, oldest first. afterID is the hex id a
// client last saw; empty means "from the oldest retained event". ErrCursorExpired
// is returned when the cursor predates the retained window.
func (s *Store) Since(ctx context.Context, channel, chatID, afterID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	filter := bson.D{
		{Key: "channel", Value: channel},
		{Key: "chat_id", Value: chatID},
	}
	if afterID != "" {
		oid, err := bson.ObjectIDFromHex(afterID)
		if err != nil {
			return nil, ErrCursorExpired
		}
		// A cursor older than the oldest retained event means events between it
		// and that event are gone: returning "the newer ones" would silently skip
		// part of a reply. Hex ids compare in creation order (fixed length,
		// timestamp first), so the comparison is a plain string compare.
		oldest, err := s.oldest(ctx, channel, chatID)
		if err != nil {
			return nil, err
		}
		if oldest != nil && oldest.Hex() > afterID {
			return nil, ErrCursorExpired
		}
		filter = append(filter, bson.E{Key: "_id", Value: bson.D{{Key: "$gt", Value: oid}}})
	}

	cursor, err := s.coll.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("read stream events: %w", err)
	}
	events := []Event{}
	if err := cursor.All(ctx, &events); err != nil {
		return nil, fmt.Errorf("decode stream events: %w", err)
	}

	return events, nil
}

// oldest returns the conversation's oldest retained id, or nil when it has none.
func (s *Store) oldest(ctx context.Context, channel, chatID string) (*bson.ObjectID, error) {
	var doc struct {
		ID bson.ObjectID `bson:"_id"`
	}
	err := s.coll.FindOne(ctx,
		bson.D{{Key: "channel", Value: channel}, {Key: "chat_id", Value: chatID}},
		options.FindOne().SetSort(bson.D{{Key: "_id", Value: 1}}).SetProjection(bson.D{{Key: "_id", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read oldest stream event: %w", err)
	}
	return &doc.ID, nil
}

// Newest returns the conversation's newest event id, or "" when it has none.
// A fresh subscriber starts from here: replaying the previous turn's answer into
// a client that just asked a new question is worse than missing nothing.
func (s *Store) Newest(ctx context.Context, channel, chatID string) (string, error) {
	var doc struct {
		ID bson.ObjectID `bson:"_id"`
	}
	err := s.coll.FindOne(ctx,
		bson.D{{Key: "channel", Value: channel}, {Key: "chat_id", Value: chatID}},
		options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}}).SetProjection(bson.D{{Key: "_id", Value: 1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read newest stream event: %w", err)
	}
	return doc.ID.Hex(), nil
}

// Trim keeps the newest keep events of one conversation and deletes the rest.
func (s *Store) Trim(ctx context.Context, channel, chatID string, keep int) error {
	if keep <= 0 {
		keep = KeepPerConversation
	}
	var boundary struct {
		ID bson.ObjectID `bson:"_id"`
	}
	err := s.coll.FindOne(ctx,
		bson.D{{Key: "channel", Value: channel}, {Key: "chat_id", Value: chatID}},
		options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}}).SetSkip(int64(keep)).SetProjection(bson.D{{Key: "_id", Value: 1}}),
	).Decode(&boundary)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil // fewer than keep events
	}
	if err != nil {
		return fmt.Errorf("read trim boundary: %w", err)
	}
	_, err = s.coll.DeleteMany(ctx, bson.D{
		{Key: "channel", Value: channel},
		{Key: "chat_id", Value: chatID},
		{Key: "_id", Value: bson.D{{Key: "$lte", Value: boundary.ID}}},
	})
	if err != nil {
		return fmt.Errorf("trim stream events: %w", err)
	}
	return nil
}
