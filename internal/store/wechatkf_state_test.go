package store_test

// The WeChat KF sync state is one document per account, and its two writers only
// ever touch their own half: moving the cursor leaves the callback token alone,
// and storing a token leaves the cursor alone. That is the part worth pinning
// against a real server — it is the SQL upsert's ON DUPLICATE KEY branch, where
// only the listed columns were written.

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// TestGetWeChatKFSyncState_NotFoundIsNotAnError: the SQL's sql.ErrNoRows became
// a nil state, and the handler turns that into Found: false — not a 500.
func TestGetWeChatKFSyncState_NotFoundIsNotAnError(t *testing.T) {
	s := storetest.Connect(t)

	state, err := s.GetWeChatKFSyncState("kf_123")
	if err != nil {
		t.Fatalf("GetWeChatKFSyncState: %v", err)
	}
	if state != nil {
		t.Fatalf("expected no state, got %#v", state)
	}
}

func TestUpdateWeChatKFCursor_InsertsTheState(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.UpdateWeChatKFCursor("kf_123", "cursor-2"); err != nil {
		t.Fatalf("UpdateWeChatKFCursor: %v", err)
	}

	state, err := s.GetWeChatKFSyncState("kf_123")
	if err != nil {
		t.Fatalf("GetWeChatKFSyncState: %v", err)
	}
	if state == nil {
		t.Fatal("the cursor was not persisted")
	}
	if !state.Found || state.OpenKfID != "kf_123" || state.Cursor != "cursor-2" {
		t.Fatalf("unexpected state: %+v", state)
	}
	if state.UpdatedAt.IsZero() {
		t.Fatal("updated_at was not set")
	}
}

// TestUpdateWeChatKFCursor_KeepsTheCallbackToken is the reason these two writers
// are separate at all: the cursor moves on every poll, and it must not wipe the
// token the callback handler stored.
func TestUpdateWeChatKFCursor_KeepsTheCallbackToken(t *testing.T) {
	s := storetest.Connect(t)

	expires := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Millisecond)
	if err := s.UpdateWeChatKFCallbackToken("kf_123", "token-2", expires); err != nil {
		t.Fatalf("UpdateWeChatKFCallbackToken: %v", err)
	}
	if err := s.UpdateWeChatKFCursor("kf_123", "cursor-9"); err != nil {
		t.Fatalf("UpdateWeChatKFCursor: %v", err)
	}

	state, err := s.GetWeChatKFSyncState("kf_123")
	if err != nil {
		t.Fatalf("GetWeChatKFSyncState: %v", err)
	}
	if state.CallbackToken != "token-2" {
		t.Fatalf("moving the cursor cleared the token: %+v", state)
	}
	if !state.TokenExpiresAt.Equal(expires) {
		t.Fatalf("expiry changed: %v want %v", state.TokenExpiresAt, expires)
	}
}

// TestUpdateWeChatKFCallbackToken_KeepsTheCursor is the other direction.
func TestUpdateWeChatKFCallbackToken_KeepsTheCursor(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.UpdateWeChatKFCursor("kf_123", "cursor-1"); err != nil {
		t.Fatalf("UpdateWeChatKFCursor: %v", err)
	}
	if err := s.UpdateWeChatKFCallbackToken("kf_123", "token-1", time.Now().UTC().Add(time.Minute)); err != nil {
		t.Fatalf("UpdateWeChatKFCallbackToken: %v", err)
	}

	state, err := s.GetWeChatKFSyncState("kf_123")
	if err != nil {
		t.Fatalf("GetWeChatKFSyncState: %v", err)
	}
	if state.Cursor != "cursor-1" {
		t.Fatalf("storing a token cleared the cursor: %+v", state)
	}
	if state.CallbackToken != "token-1" {
		t.Fatalf("token not stored: %+v", state)
	}
}

// TestUpdateWeChatKFCallbackToken_ZeroExpiryMeansNoExpiry: the SQL wrote NULL
// for a zero time, and a reader must see "no expiry" rather than the zero
// instant (which would read as expired).
func TestUpdateWeChatKFCallbackToken_ZeroExpiryMeansNoExpiry(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.UpdateWeChatKFCallbackToken("kf_123", "token-3", time.Time{}); err != nil {
		t.Fatalf("UpdateWeChatKFCallbackToken: %v", err)
	}
	state, err := s.GetWeChatKFSyncState("kf_123")
	if err != nil {
		t.Fatalf("GetWeChatKFSyncState: %v", err)
	}
	if !state.TokenExpiresAt.IsZero() {
		t.Fatalf("expected no expiry, got %v", state.TokenExpiresAt)
	}

	// And going back to a real expiry after "no expiry" works both ways.
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Millisecond)
	if err := s.UpdateWeChatKFCallbackToken("kf_123", "token-4", expires); err != nil {
		t.Fatalf("UpdateWeChatKFCallbackToken: %v", err)
	}
	state, err = s.GetWeChatKFSyncState("kf_123")
	if err != nil {
		t.Fatalf("GetWeChatKFSyncState: %v", err)
	}
	if state.CallbackToken != "token-4" || !state.TokenExpiresAt.Equal(expires) {
		t.Fatalf("unexpected state after re-storing a token: %+v", state)
	}
}

func TestWeChatKFState_IsOneDocumentPerAccount(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.UpdateWeChatKFCursor("kf_1", "c1"); err != nil {
		t.Fatalf("UpdateWeChatKFCursor: %v", err)
	}
	if err := s.UpdateWeChatKFCursor("kf_2", "c2"); err != nil {
		t.Fatalf("UpdateWeChatKFCursor: %v", err)
	}
	// Polling the same account repeatedly must not accumulate documents.
	for i := 0; i < 3; i++ {
		if err := s.UpdateWeChatKFCursor("kf_1", "c1"); err != nil {
			t.Fatalf("UpdateWeChatKFCursor: %v", err)
		}
	}

	n, err := s.DB().Collection("wechatkf_sync_state").CountDocuments(context.Background(), bson.D{})
	if err != nil {
		t.Fatalf("count states: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected one document per account (2), got %d", n)
	}
}
