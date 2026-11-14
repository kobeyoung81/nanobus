package stream

// Inbound notices.
//
// An agent used to ask "anything for me?" twice a second. The queue row is the
// truth, and the claim is atomic, so the push does not need to carry the message
// — it only needs to say that one exists. This tailer turns an insert on
// bus_inbound_queue into such a notice, on every replica, so the one holding the
// agent's socket is the one that pushes and no routing layer is needed.
//
// A replica that was not following (restart, no change streams) still answers:
// the agent's fallback poll asks the queue directly.

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// InboundNotice is the part of a queued message a push needs: who it is for.
type InboundNotice struct {
	ID      bson.ObjectID `bson:"_id,omitempty"`
	UserID  string        `bson:"user_id"`
	AgentID string        `bson:"agent_id"`
	Status  string        `bson:"status"`
}

// NewInboundTailer tails the inbound queue for inserts.
//
// The scan does not filter on status: a row that was claimed between the insert
// and the read must still move the cursor, otherwise a busy queue would keep
// re-reading the same page. The caller filters.
func NewInboundTailer(coll *mongo.Collection) *Tailer[InboundNotice] {
	return &Tailer[InboundNotice]{
		coll:   coll,
		decode: decodeInboundNotice,
		idOf:   func(n InboundNotice) string { return n.ID.Hex() },
		subjectOf: func(n InboundNotice) string {
			return n.UserID + "|" + n.AgentID
		},
		scan: func(ctx context.Context, cursor string, limit int) ([]InboundNotice, error) {
			after, err := bson.ObjectIDFromHex(cursor)
			if err != nil {
				return nil, err
			}
			opts := options.Find().
				SetSort(bson.D{{Key: "_id", Value: 1}}).
				SetLimit(int64(limit))
			cur, err := coll.Find(ctx, bson.D{{Key: "_id", Value: bson.D{{Key: "$gt", Value: after}}}}, opts)
			if err != nil {
				return nil, err
			}
			defer cur.Close(ctx)
			var out []InboundNotice
			for cur.Next(ctx) {
				var n InboundNotice
				if err := cur.Decode(&n); err != nil {
					continue
				}
				out = append(out, n)
			}
			return out, cur.Err()
		},
		newest: func(ctx context.Context) (string, error) {
			opts := options.FindOne().SetSort(bson.D{{Key: "_id", Value: -1}})
			var n InboundNotice
			err := coll.FindOne(ctx, bson.D{}, opts).Decode(&n)
			if err == mongo.ErrNoDocuments {
				return "", nil
			}
			if err != nil {
				return "", err
			}
			return n.ID.Hex(), nil
		},
		scanEvery: 2 * time.Second,
		subs:      make(map[int]*TailSub[InboundNotice]),
	}
}

func decodeInboundNotice(raw bson.Raw) (InboundNotice, bool) {
	var n InboundNotice
	if err := bson.Unmarshal(raw, &n); err != nil {
		return n, false
	}
	return n, n.UserID != ""
}
