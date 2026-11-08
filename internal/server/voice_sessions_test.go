package server

import (
	"testing"
	"time"
)

func TestVoiceSessionMintAndClaim(t *testing.T) {
	reg := newVoiceSessionRegistry(time.Minute)

	id, err := reg.Mint("user-1")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if id == "" {
		t.Fatal("Mint returned an empty id")
	}

	userID, ok := reg.Claim(id)
	if !ok || userID != "user-1" {
		t.Fatalf("Claim = (%q, %v), want (user-1, true)", userID, ok)
	}
}

func TestVoiceSessionIsSingleUse(t *testing.T) {
	reg := newVoiceSessionRegistry(time.Minute)
	id, _ := reg.Mint("user-1")
	if _, ok := reg.Claim(id); !ok {
		t.Fatal("first claim must succeed")
	}
	if _, ok := reg.Claim(id); ok {
		t.Fatal("a second claim of the same id must be refused")
	}
}

func TestVoiceSessionExpires(t *testing.T) {
	reg := newVoiceSessionRegistry(-time.Second)
	id, _ := reg.Mint("user-1")
	if _, ok := reg.Claim(id); ok {
		t.Fatal("an expired id must be refused")
	}
}

func TestVoiceSessionUnknownID(t *testing.T) {
	reg := newVoiceSessionRegistry(time.Minute)
	reg.Mint("user-1")
	if _, ok := reg.Claim("not-a-minted-id"); ok {
		t.Fatal("an unknown id must be refused")
	}
}

func TestVoiceSessionMintPrunesExpired(t *testing.T) {
	reg := newVoiceSessionRegistry(-time.Second)
	first, _ := reg.Mint("user-1")

	reg.ttl = time.Minute
	second, _ := reg.Mint("user-2")

	if _, ok := reg.Claim(first); ok {
		t.Fatal("the pruned id must no longer be claimable")
	}
	if userID, ok := reg.Claim(second); !ok || userID != "user-2" {
		t.Fatalf("Claim(second) = (%q, %v), want (user-2, true)", userID, ok)
	}
}
