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

// wechatKFSyncStateCollection needs no migration manifest: the only access path
// is by open_kfid, which is the document's _id, and MongoDB indexes _id on
// creation. A manifest would declare nothing.
const wechatKFSyncStateCollection = "bus_wechatkf_sync_state"

// storedWeChatKFState is the document shape.
//
// The SQL columns defaulted to the empty string and to TIMESTAMP NULL. Here an
// unset callback_token and an absent token_expires_at mean the same thing: the
// zero string and "no expiry" are what the Go type carries either way, so the
// defaults are not written out just to be read back.
type storedWeChatKFState struct {
	OpenKfID       string     `bson:"_id"`
	Cursor         string     `bson:"cursor"`
	CallbackToken  string     `bson:"callback_token,omitempty"`
	TokenExpiresAt *time.Time `bson:"token_expires_at,omitempty"`
	UpdatedAt      time.Time  `bson:"updated_at"`
}

// GetWeChatKFSyncState returns the shared sync state for a WeChat KF account.
func (s *Store) GetWeChatKFSyncState(openKfID string) (*nanoprotocol.WeChatKFSyncStateResponse, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedWeChatKFState
	err := s.db.Collection(wechatKFSyncStateCollection).
		FindOne(ctx, bson.D{{Key: "_id", Value: openKfID}}).Decode(&doc)
	// "No state yet" is not an error — it is what the SQL's sql.ErrNoRows was
	// translated into, and the handler turns a nil state into Found: false.
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get wechatkf sync state: %w", err)
	}

	state := &nanoprotocol.WeChatKFSyncStateResponse{
		Found:         true,
		OpenKfID:      openKfID,
		Cursor:        doc.Cursor,
		CallbackToken: doc.CallbackToken,
		UpdatedAt:     doc.UpdatedAt,
	}
	if doc.TokenExpiresAt != nil {
		state.TokenExpiresAt = *doc.TokenExpiresAt
	}
	return state, nil
}

// UpdateWeChatKFCursor creates or updates the persisted sync cursor for a WeChat KF account.
func (s *Store) UpdateWeChatKFCursor(openKfID, cursor string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// Only the cursor is written: the SQL was an upsert whose ON DUPLICATE KEY
	// branch touched `cursor` and `updated_at` alone, so a stored callback token
	// survives a cursor move.
	_, err := s.db.Collection(wechatKFSyncStateCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: openKfID}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "cursor", Value: cursor},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("update wechatkf cursor: %w", err)
	}
	return nil
}

// UpdateWeChatKFCallbackToken creates or updates the short-lived callback token for a WeChat KF account.
func (s *Store) UpdateWeChatKFCallbackToken(openKfID, callbackToken string, tokenExpiresAt time.Time) error {
	ctx, cancel := opCtx()
	defer cancel()

	set := bson.D{
		{Key: "callback_token", Value: callbackToken},
		{Key: "updated_at", Value: time.Now().UTC()},
	}
	var update bson.D
	if tokenExpiresAt.IsZero() {
		// The SQL wrote NULL for a zero time. An absent field is the same
		// thing here, and it reads back as the same nil *time.Time.
		update = bson.D{
			{Key: "$set", Value: set},
			{Key: "$unset", Value: bson.D{{Key: "token_expires_at", Value: ""}}},
		}
	} else {
		update = bson.D{{Key: "$set", Value: append(set,
			bson.E{Key: "token_expires_at", Value: tokenExpiresAt.UTC()})}}
	}

	// The cursor is left alone, again mirroring the SQL's ON DUPLICATE KEY branch.
	_, err := s.db.Collection(wechatKFSyncStateCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: openKfID}}, update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("update wechatkf callback token: %w", err)
	}
	return nil
}

// CountWeChatKFAccounts returns how many KF accounts have sync state stored. The
// console's component list shows it as the gateway's WeChat channel count.
func (s *Store) CountWeChatKFAccounts() (int, error) {
	ctx, cancel := opCtx()
	defer cancel()

	count, err := s.db.Collection(wechatKFSyncStateCollection).CountDocuments(ctx, bson.D{})
	if err != nil {
		return 0, fmt.Errorf("count wechatkf accounts: %w", err)
	}
	return int(count), nil
}
