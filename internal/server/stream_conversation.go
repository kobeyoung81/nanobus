package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/metrics"
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
func streamStoreFor(st *store.Store) *stream.Store {
	if st == nil {
		return nil
	}
	return stream.NewStore(st.DB())
}

// conversationSubject is the broadcast key for one conversation. The legacy
// channel subject is still fed so gateways that have not been restarted keep
// receiving events.
func conversationSubject(channel, chatID string) string { return channel + "\x00" + chatID }

// coalescerFor builds the write coalescer when a database is configured.
func coalescerFor(streams streamEventStore) *stream.Coalescer {
	appender, ok := streams.(stream.Appender)
	if !ok {
		return nil
	}
	return stream.NewCoalescer(appender, 200*time.Millisecond)
}

// recordAndBroadcast persists one event and wakes every subscriber: the
// conversation's own (durable, replayable) and the channel's legacy one.
func (s *Server) recordAndBroadcast(ctx context.Context, ev stream.ChunkEvent) error {
	metrics.StreamEventsWritten.WithLabelValues(ev.Type).Inc()
	var appendErr error
	if s.coalescer != nil {
		// The coalescer writes in the background; the caller's answer does not
		// wait for the database.
		s.coalescer.Add(stream.Event{
			Channel: ev.Channel, ChatID: ev.ChatID, Type: ev.Type, Kind: ev.Kind,
			MessageID: ev.MessageID, Seq: ev.Seq, Content: ev.Content,
			FullContent: ev.FullContent, Summary: ev.Summary,
			Error: ev.Error, ErrorDetail: ev.ErrorDetail,
			AgentID: ev.AgentID, UserID: ev.UserID, SessionID: ev.SessionID,
		})
	} else if s.streams != nil {
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

// streamConversation serves one conversation's events.
//
// The log is the cursor of record: what a client missed is read from it once,
// then the replica's tailer pushes what happens next. Polling the collection per
// subscriber is the fallback for a server without a tailer (tests, degraded
// start).
func (s *Server) streamConversation(ctx context.Context, sw *sseWriter, r *http.Request, channel, chatID string, lastID string) {
	metrics.StreamSubscribers.Inc()
	defer metrics.StreamSubscribers.Dec()

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

	if s.eventTailer == nil {
		s.streamConversationPolling(ctx, sw, channel, chatID, lastID)
		return
	}

	// Subscribe before replaying: anything that happens during the replay is
	// buffered, and the id guard below drops the overlap instead of losing it.
	sub := s.eventTailer.Subscribe(conversationSubject(channel, chatID))
	defer sub.Close()

	if err := s.replayConversation(ctx, sw, channel, chatID, &lastID); err != nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			if err := s.sendConversationEvent(sw, ev, &lastID); err != nil {
				return
			}
		case <-sub.Lag():
			// The tailer dropped events for this subscriber: read the log from
			// where it stopped instead of leaving a hole in the answer.
			if err := s.replayConversation(ctx, sw, channel, chatID, &lastID); err != nil {
				return
			}
		}
	}
}

// sendConversationEvent writes one event unless the client already has it.
func (s *Server) sendConversationEvent(sw *sseWriter, ev stream.Event, lastID *string) error {
	hex := ev.ID.Hex()
	if hex == "" || hex <= *lastID {
		return nil
	}
	out := ev.ToChunkEvent()
	if err := sw.event(out.Type, hex, out); err != nil {
		return err
	}
	*lastID = hex
	return nil
}

// replayConversation catches the client up from the log. A cursor older than
// the retained window tells the client to reset, then continues from what is
// still there.
func (s *Server) replayConversation(ctx context.Context, sw *sseWriter, channel, chatID string, lastID *string) error {
	for {
		events, err := s.streams.Since(ctx, channel, chatID, *lastID, 200)
		if err != nil {
			if err == stream.ErrCursorExpired {
				if err := sw.event("reset", "", map[string]any{
					"type":    "reset",
					"reason":  "cursor_expired",
					"channel": channel,
					"chat_id": chatID,
				}); err != nil {
					return err
				}
				*lastID = ""
				continue
			}
			slog.Warn("stream: read conversation events failed", "channel", channel, "chat_id", chatID, "error", err)
			return nil
		}
		for _, ev := range events {
			if err := s.sendConversationEvent(sw, ev, lastID); err != nil {
				return err
			}
		}
		if len(events) < 200 {
			return nil
		}
	}
}

// streamConversationPolling is the no-tailer path: replay and live delivery are
// the same query, woken early by a local write.
func (s *Server) streamConversationPolling(ctx context.Context, sw *sseWriter, channel, chatID string, lastID string) {
	sub := s.sse.subscribe(conversationSubject(channel, chatID))
	defer s.sse.unsubscribe(conversationSubject(channel, chatID), sub)

	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()

	for {
		events, err := s.streams.Since(ctx, channel, chatID, lastID, 200)
		if err != nil {
			if err == stream.ErrCursorExpired {
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
			if err := s.sendConversationEvent(sw, ev, &lastID); err != nil {
				return
			}
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
