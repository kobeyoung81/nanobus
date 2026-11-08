package server

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// voiceSessionTTL bounds how long a minted id stays usable. The browser uses it
// within a second of asking for it; the window only needs to survive the
// round trip.
const voiceSessionTTL = 60 * time.Second

// voiceSessionRegistry mints the one-time ids that open a voice WebSocket.
//
// The bus has no authentication of its own (it lives on the internal network
// and trusts the portal to identify callers), so for the voice socket the id
// *is* the capability: only the portal can ask for one, the bus accepts only an
// id it minted, and each id works exactly once.
type voiceSessionRegistry struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	ids map[string]voiceSession
}

type voiceSession struct {
	userID    string
	expiresAt time.Time
}

func newVoiceSessionRegistry(ttl time.Duration) *voiceSessionRegistry {
	return &voiceSessionRegistry{
		ttl: ttl,
		now: time.Now,
		ids: make(map[string]voiceSession),
	}
}

// Mint issues a fresh id for the user, pruning everything that has expired.
func (r *voiceSessionRegistry) Mint(userID string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf)

	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for key, session := range r.ids {
		if now.After(session.expiresAt) {
			delete(r.ids, key)
		}
	}
	r.ids[id] = voiceSession{userID: userID, expiresAt: now.Add(r.ttl)}
	return id, nil
}

// Claim consumes an id, reporting the user it was minted for. A missing,
// expired or already-used id is refused.
func (r *voiceSessionRegistry) Claim(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	session, ok := r.ids[id]
	if !ok {
		return "", false
	}
	delete(r.ids, id)
	if r.now().After(session.expiresAt) {
		return "", false
	}
	return session.userID, true
}
