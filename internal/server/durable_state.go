package server

// Durable platform events and voice ids.
//
// Both used to live in this process: a change notification published on one bus
// replica never reached a subscriber on another, and a voice id minted by the
// portal's request to replica A was unknown to replica B when the browser opened
// its socket there. Both are now rows, with small interfaces so the handlers can
// be tested without a database.

import (
	"context"
	"log/slog"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/stream"
)

// platformEventStore is the change-notification log.
type platformEventStore interface {
	Append(ctx context.Context, eventType, content string) (string, error)
	Since(ctx context.Context, afterID string, limit int) ([]stream.PlatformEvent, error)
	Newest(ctx context.Context) (string, error)
}

// voiceSessionStore holds the one-time voice ids.
type voiceSessionStore interface {
	Mint(ctx context.Context, userID string, ttl time.Duration) (string, error)
	Claim(ctx context.Context, id string) (userID string, ok bool, err error)
}

func platformStoreFor(st *store.Store) *stream.PlatformStore {
	if st == nil {
		return nil
	}
	return stream.NewPlatformStore(st.DB())
}

func voiceStoreFor(st *store.Store) voiceSessionStore {
	if st == nil {
		return nil
	}
	return store.NewVoiceSessionStore(st.DB())
}

// recordPlatformEvent persists one notification and wakes the local
// subscribers. Persisting is best effort: a notification lost to a database
// hiccup costs a re-read, not correctness, and failing the emitting request
// would turn a config save into an error for no benefit.
func (s *Server) recordPlatformEvent(eventType, content string) {
	if s.platform != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if _, err := s.platform.Append(ctx, eventType, content); err != nil {
			slog.Warn("platform event: persist failed", "type", eventType, "error", err)
		}
		cancel()
	}
	s.events.broadcast("events", stream.ChunkEvent{Type: eventType, Content: content})
}

// mintVoiceSession prefers the database, so the id is valid on every replica.
func (s *Server) mintVoiceSession(ctx context.Context, userID string) (string, error) {
	if s.voiceStore != nil {
		return s.voiceStore.Mint(ctx, userID, voiceSessionTTL)
	}
	return s.voiceSessions.Mint(userID)
}

// claimVoiceSession consumes the id wherever it was minted.
func (s *Server) claimVoiceSession(ctx context.Context, id string) (string, bool) {
	if s.voiceStore != nil {
		userID, ok, err := s.voiceStore.Claim(ctx, id)
		if err != nil {
			slog.Warn("voice session: claim failed", "error", err)
			return "", false
		}
		return userID, ok
	}
	return s.voiceSessions.Claim(id)
}
