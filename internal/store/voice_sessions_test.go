package store_test

// The voice id is a capability: minted once, usable once, on any replica.
// External test package on purpose: the in-package one cannot import
// storetest, which imports this package.

import (
	"context"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestVoiceSessionMintAndClaimOnce(t *testing.T) {
	st := storetest.Connect(t)
	s := store.NewVoiceSessionStore(st.DB())
	ctx := context.Background()

	id, err := s.Mint(ctx, "user-1", time.Minute)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	userID, ok, err := s.Claim(ctx, id)
	if err != nil || !ok || userID != "user-1" {
		t.Fatalf("claim = %q/%v (err %v)", userID, ok, err)
	}
	if _, ok, _ := s.Claim(ctx, id); ok {
		t.Fatal("the id was accepted twice")
	}
	if _, ok, _ := s.Claim(ctx, "never-minted"); ok {
		t.Fatal("an unknown id was accepted")
	}
}

func TestVoiceSessionExpiredIsRefused(t *testing.T) {
	st := storetest.Connect(t)
	s := store.NewVoiceSessionStore(st.DB())
	ctx := context.Background()

	if _, err := st.DB().Collection(store.VoiceSessionsCollection).InsertOne(ctx, bson.D{
		{Key: "_id", Value: "expired-id"},
		{Key: "user_id", Value: "user-1"},
		{Key: "expires_at", Value: time.Now().UTC().Add(-time.Second)},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, ok, _ := s.Claim(ctx, "expired-id"); ok {
		t.Fatal("an expired id was accepted")
	}
}
