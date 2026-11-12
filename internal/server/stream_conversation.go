package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/stream"
)

// streamEventStore is the durable event log the SSE route reads and the chunk
// routes write. It is an interface so the HTTP behaviour can be tested without
// a database.
type streamEventStore interface {
	Append(ctx context.Context, ev stream.Event) (string, error)
	Since(ctx context.Context, channel, chatID, afterID string, limit int) ([]stream.Event, error)
	Newest(ctx context.Context, channel, chatID string) (string, error)
}

// streamStoreFor builds the store from the server's database. A server without
// a store (tests, or a deliberately degraded start) keeps the in-memory ring
// only, which is what the pre-stage-5 behaviour was.
func streamStoreFor(st *store.Store) streamEventStore {
	if st == nil {
		return nil
	}
	return stream.NewStore(st.DB())
}

// conversationSubject is the broadcast key for one conversation. The legacy
// channel subject is still fed so gateways that have not been restarted keep
// receiving events.
func conversationSubject(channel, chatID string) string { return channel + "\x00" + chatID }

// recordAndBroadcast persists one event and wakes every subscriber: the
// conversation's own (durable, replayable) and the channel's legacy one.
func (s *Server) recordAndBroadcast(ctx context.Context, ev stream.ChunkEvent) error {
	var appendErr error
	if s.streams != nil {
		writeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		_, appendErr = s.streams.Append(writeCtx, stream.Event{
			Channel:     ev.Channel,
			ChatID:      ev.ChatID,
			Type:        ev.Type,
			Kind:        ev.Kind,
			MessageID:   ev.MessageID,
			Seq:         ev.Seq,
			Content:     ev.Content,
			FullContent: ev.FullContent,
			Summary:     ev.Summary,
			Error:       ev.Error,
			ErrorDetail: ev.ErrorDetail,
			AgentID:     ev.AgentID,
			UserID:      ev.UserID,
			SessionID:   ev.SessionID,
		})
		cancel()
		if appendErr != nil {
			slog.Warn("stream: persist event failed", "channel", ev.Channel, "chat_id", ev.ChatID, "error", appendErr)
		}
	}
	s.sse.broadcast(ev.Channel, ev)
	s.sse.broadcast(conversationSubject(ev.Channel, ev.ChatID), ev)
	return appendErr
}

// streamConversation serves one conversation's events from the durable log.
//
// Polling (250 ms) plus a local wake-up covers both cases without a change
// stream: a subscriber on the replica that wrote the event wakes immediately,
// and one on another replica picks it up on the next poll. Replay and live
// delivery are the same query, so a reconnect is just a different starting id.
func (s *Server) streamConversation(ctx context.Context, sw *sseWriter, r *http.Request, channel, chatID string, lastID string) {
	sub := s.sse.subscribe(conversationSubject(channel, chatID))
	defer s.sse.unsubscribe(conversationSubject(channel, chatID), sub)

	// A cursor this process cannot parse cannot be resumed from.
	if lastID != "" {
		if _, err := stream.ParseCursor(lastID); err != nil {
			if err := sw.event("reset", "", map[string]any{
				"type":    "reset",
				"reason":  "invalid_cursor",
				"channel": channel,
				"chat_id": chatID,
			}); err != nil {
				return
			}
			lastID = ""
		}
	}

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()

	for {
		events, err := s.streams.Since(ctx, channel, chatID, lastID, 200)
		if err != nil {
			if err == stream.ErrCursorExpired {
				// The client was away longer than the retained window. Tell it to
				// reset, then continue from what we still have.
				if err := sw.event("reset", "", map[string]any{
					"type":    "reset",
					"reason":  "cursor_expired",
					"channel": channel,
					"chat_id": chatID,
				}); err != nil {
					return
				}
				lastID = ""
				continue
			}
			slog.Warn("stream: read conversation events failed", "channel", channel, "chat_id", chatID, "error", err)
		}
		for _, ev := range events {
			out := ev.ToChunkEvent()
			if err := sw.event(out.Type, ev.ID.Hex(), out); err != nil {
				return
			}
			lastID = ev.ID.Hex()
		}
		if len(events) > 0 {
			continue
		}

		select {
		case <-ctx.Done():
			return
		case <-sub.ch:
			// A local writer woke us: read again immediately.
		case <-poll.C:
		}
	}
}
