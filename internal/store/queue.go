package store

import (
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanoprotocol"
)

const (
	inboundQueueCollection  = "bus_inbound_queue"
	outboundQueueCollection = "bus_outbound_queue"
)

// storedMediaItem is one attachment as stored.
//
// An explicit mapping because the coincidence does not hold here: the driver
// would take the field names of nanoprotocol.MediaItem lowered (MediaID →
// "mediaid", MimeType → "mimetype"), which are not the document's field names.
type storedMediaItem struct {
	Platform string `bson:"platform,omitempty"`
	MediaID  string `bson:"media_id,omitempty"`
	Filename string `bson:"filename,omitempty"`
	MimeType string `bson:"mime_type,omitempty"`
	URL      string `bson:"url,omitempty"`
}

// storedQueuedMessage is the document shape of both queues.
//
// One shape for two queues: they list the same things, and the fields only one
// side uses (sender_id for inbound, reply_to for outbound) simply stay empty.
type storedQueuedMessage struct {
	// The SQL id was an auto-increment nothing outside the claim statement used
	// (the portal counts rows and takes MAX(created_at)), so the document keeps
	// MongoDB's own ObjectID rather than spending a counter on it.
	ID        bson.ObjectID `bson:"_id,omitempty"`
	UserID    string        `bson:"user_id"`
	BindingID *int64        `bson:"binding_id,omitempty"`
	// Written even when empty, unlike the other optional fields: the claim's
	// filter compares agent_id to "", and an absent field would need `null` in
	// that comparison — the SQL column was NOT NULL DEFAULT ''.
	AgentID   string            `bson:"agent_id"`
	SessionID string            `bson:"session_id"`
	Channel   string            `bson:"channel"`
	ChatID    string            `bson:"chat_id"`
	SenderID  string            `bson:"sender_id,omitempty"`
	MessageID string            `bson:"message_id,omitempty"`
	Content   string            `bson:"content"`
	Media     []storedMediaItem `bson:"media,omitempty"`
	ReplyTo   string            `bson:"reply_to,omitempty"`
	// Metadata is passed through untouched, and nested values in it reach the
	// wire as JSON — which is why both queue collections are opened with
	// DefaultDocumentM (see the queue accessors).
	Metadata  map[string]any `bson:"metadata,omitempty"`
	Status    string         `bson:"status"`
	ClaimedBy string         `bson:"claimed_by,omitempty"`
	ClaimedAt *time.Time     `bson:"claimed_at,omitempty"`
	CreatedAt time.Time      `bson:"created_at"`
}

// InboundQueueCollection exposes the inbound queue to the notice tailer: the
// bus pushes "a message is waiting" from the inserts it sees here, and the
// atomic claim stays inside this package.
func (s *Store) InboundQueueCollection() *mongo.Collection { return s.inboundQueue() }

// inboundQueue/outboundQueue are the two collections, configured so a decoded
// metadata document comes back as a map rather than the driver's bson.D (a list
// of pairs, which json.Marshal turns into an array of {"Key":…,"Value":…}
// objects). Same reason as auditLog.
func (s *Store) inboundQueue() *mongo.Collection {
	return s.db.Collection(inboundQueueCollection,
		options.Collection().SetBSONOptions(&options.BSONOptions{DefaultDocumentM: true}))
}

func (s *Store) outboundQueue() *mongo.Collection {
	return s.db.Collection(outboundQueueCollection,
		options.Collection().SetBSONOptions(&options.BSONOptions{DefaultDocumentM: true}))
}

func (d storedQueuedMessage) mediaItems() []nanoprotocol.MediaItem {
	if len(d.Media) == 0 {
		return nil
	}
	out := make([]nanoprotocol.MediaItem, 0, len(d.Media))
	for _, m := range d.Media {
		out = append(out, nanoprotocol.MediaItem{
			Platform: m.Platform, MediaID: m.MediaID,
			Filename: m.Filename, MimeType: m.MimeType, URL: m.URL,
		})
	}
	return out
}

func storedMediaItems(items []nanoprotocol.MediaItem) []storedMediaItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]storedMediaItem, 0, len(items))
	for _, m := range items {
		out = append(out, storedMediaItem{
			Platform: m.Platform, MediaID: m.MediaID,
			Filename: m.Filename, MimeType: m.MimeType, URL: m.URL,
		})
	}
	return out
}

// inboundMessage converts a claimed document into the message the caller gets.
func (d storedQueuedMessage) inboundMessage() *nanoprotocol.InboundMessage {
	msg := &nanoprotocol.InboundMessage{
		UserID: d.UserID, AgentID: d.AgentID,
		SessionID: d.SessionID, Channel: d.Channel, ChatID: d.ChatID,
		SenderID: d.SenderID, MessageID: d.MessageID, Content: d.Content,
		Media: d.mediaItems(), Metadata: d.Metadata, Timestamp: d.CreatedAt,
	}
	if d.BindingID != nil {
		msg.BindingID = *d.BindingID
	}
	if msg.MessageID == "" {
		msg.MessageID = messageIDFromMetadata(msg.Metadata)
	}
	return msg
}

// outboundMessage converts a claimed document into the message the caller gets.
func (d storedQueuedMessage) outboundMessage(channel string) *nanoprotocol.OutboundMessage {
	msg := &nanoprotocol.OutboundMessage{
		UserID: d.UserID, AgentID: d.AgentID,
		SessionID: d.SessionID, Channel: channel, ChatID: d.ChatID,
		MessageID: d.MessageID, Content: d.Content,
		Media: d.mediaItems(), ReplyTo: d.ReplyTo, Metadata: d.Metadata,
	}
	if d.BindingID != nil {
		msg.BindingID = *d.BindingID
	}
	return msg
}

// PostInbound inserts a message into the inbound queue.
func (s *Store) PostInbound(msg nanoprotocol.InboundMessage) error {
	ctx, cancel := opCtx()
	defer cancel()

	doc := storedQueuedMessage{
		UserID: msg.UserID, BindingID: nullableInt64(msg.BindingID), AgentID: msg.AgentID,
		SessionID: msg.SessionID, Channel: msg.Channel, ChatID: msg.ChatID,
		SenderID: msg.SenderID, MessageID: msg.MessageID, Content: msg.Content,
		Media: storedMediaItems(msg.Media), Metadata: msg.Metadata,
		// 'pending' and the timestamp were column defaults; the document states
		// them, because a claim's filter depends on the first one.
		Status:    "pending",
		CreatedAt: time.Now().UTC(),
	}
	if _, err := s.inboundQueue().InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("post inbound: %w", err)
	}
	return nil
}

// ClaimInbound atomically claims the oldest pending inbound message for a user.
// Returns nil, nil if no pending messages exist.
func (s *Store) ClaimInbound(userID string, bindingID int64, agentID string) (*nanoprotocol.InboundMessage, error) {
	ctx, cancel := opCtx()
	defer cancel()

	filter := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "status", Value: "pending"},
	}
	// `(? = 0 OR binding_id = ? OR binding_id IS NULL)`: a binding of 0 means
	// "any binding", and a message that carries none matches too. $in with null
	// is exactly that — MongoDB matches a null in the list against a missing
	// field as well.
	if bindingID != 0 {
		filter = append(filter, bson.E{Key: "binding_id",
			Value: bson.D{{Key: "$in", Value: bson.A{bindingID, nil}}}})
	}
	// `(? = '' OR agent_id = '' OR agent_id = ?)`, same shape.
	if agentID != "" {
		filter = append(filter, bson.E{Key: "agent_id",
			Value: bson.D{{Key: "$in", Value: bson.A{"", agentID}}}})
	}

	// SELECT ... FOR UPDATE followed by an UPDATE, in one operation: the filter
	// that finds the message is the filter that claims it, so two claimants
	// cannot be handed the same row — each one gets a different document or
	// none at all. A standalone server has no transaction to do this with.
	now := time.Now().UTC()
	var doc storedQueuedMessage
	err := s.inboundQueue().FindOneAndUpdate(ctx, filter,
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "status", Value: "claimed"},
			{Key: "claimed_by", Value: agentID},
			{Key: "claimed_at", Value: now},
		}}},
		options.FindOneAndUpdate().
			// _id breaks the tie: a BSON timestamp has millisecond resolution,
			// and a burst of messages posted together would otherwise come out in
			// whatever order the server happened to read them. ObjectIDs increase
			// with insertion, so this keeps the queue first-in-first-out.
			SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}).
			SetReturnDocument(options.After),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim inbound: %w", err)
	}
	return doc.inboundMessage(), nil
}

// PostOutbound inserts a message into the outbound queue.
func (s *Store) PostOutbound(msg nanoprotocol.OutboundMessage) error {
	if msg.MessageID == "" {
		return fmt.Errorf("missing message_id")
	}
	ctx, cancel := opCtx()
	defer cancel()

	doc := storedQueuedMessage{
		UserID: msg.UserID, BindingID: nullableInt64(msg.BindingID), AgentID: msg.AgentID,
		SessionID: msg.SessionID, Channel: msg.Channel, ChatID: msg.ChatID,
		MessageID: msg.MessageID, Content: msg.Content, ReplyTo: msg.ReplyTo,
		Media: storedMediaItems(msg.Media), Metadata: msg.Metadata,
		Status:    "pending",
		CreatedAt: time.Now().UTC(),
	}
	if _, err := s.outboundQueue().InsertOne(ctx, doc); err != nil {
		return fmt.Errorf("post outbound: %w", err)
	}
	return nil
}

// MarkOutboundDone marks a posted outbound message as delivered. Delivery for
// streaming channels happens via the SSE broadcast, so the queue row is marked
// done immediately after broadcast rather than left pending forever.
func (s *Store) MarkOutboundDone(messageID string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// updateMany, not updateOne: the SQL UPDATE had no LIMIT, so every row with
	// that message id was marked.
	if _, err := s.outboundQueue().UpdateMany(ctx,
		bson.D{{Key: "message_id", Value: messageID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "done"}}}},
	); err != nil {
		return fmt.Errorf("mark outbound done: %w", err)
	}
	return nil
}

// ClaimOutbound atomically claims the oldest pending outbound message for a channel.
// Returns nil, nil if no pending messages exist.
func (s *Store) ClaimOutbound(channel, gatewayID string) (*nanoprotocol.OutboundMessage, error) {
	ctx, cancel := opCtx()
	defer cancel()

	now := time.Now().UTC()
	var doc storedQueuedMessage
	err := s.outboundQueue().FindOneAndUpdate(ctx,
		bson.D{
			{Key: "channel", Value: channel},
			{Key: "status", Value: "pending"},
		},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "status", Value: "claimed"},
			{Key: "claimed_by", Value: gatewayID},
			{Key: "claimed_at", Value: now},
		}}},
		options.FindOneAndUpdate().
			SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}).
			SetReturnDocument(options.After),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim outbound: %w", err)
	}
	return doc.outboundMessage(channel), nil
}

// messageIDFromMetadata reads the platform's message id out of a message's
// metadata when the sender did not carry it as a field of its own.
func messageIDFromMetadata(metadata map[string]any) string {
	for _, key := range []string{"message_id", "msg_id"} {
		if value, ok := metadata[key]; ok {
			if messageID, ok := value.(string); ok {
				return messageID
			}
			return fmt.Sprint(value)
		}
	}
	return ""
}

// nullableInt64 turns the protocol's zero-means-absent binding id into the
// document's absent field, the way NULL did in SQL.
func nullableInt64(value int64) *int64 {
	if value == 0 {
		return nil
	}
	return &value
}

// CountPendingQueues returns how many messages are waiting in each direction. The
// console's overview prints the two as "inbound backlog" and "outbound backlog".
func (s *Store) CountPendingQueues() (inbound, outbound int, err error) {
	ctx, cancel := opCtx()
	defer cancel()

	in, err := s.inboundQueue().CountDocuments(ctx, bson.D{{Key: "status", Value: "pending"}})
	if err != nil {
		return 0, 0, fmt.Errorf("count pending inbound: %w", err)
	}
	out, err := s.outboundQueue().CountDocuments(ctx, bson.D{{Key: "status", Value: "pending"}})
	if err != nil {
		return 0, 0, fmt.Errorf("count pending outbound: %w", err)
	}
	return int(in), int(out), nil
}
