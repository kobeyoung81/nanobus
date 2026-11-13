package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/metrics"
	"gitee.com/kobeyoung81/nanobus/internal/stream"
	"gitee.com/kobeyoung81/nanoprotocol"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// sseSubscriber receives broadcast events for a subject.
type sseSubscriber struct {
	id int
	ch chan stream.ChunkEvent
}

// sseHub manages per-subject subscribers.
type sseHub struct {
	mu          sync.Mutex
	nextID      int
	subscribers map[string]map[int]*sseSubscriber
}

func newSSEHub() *sseHub {
	return &sseHub{subscribers: make(map[string]map[int]*sseSubscriber)}
}

func (h *sseHub) subscribe(subject string) *sseSubscriber {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	sub := &sseSubscriber{id: h.nextID, ch: make(chan stream.ChunkEvent, 64)}
	if h.subscribers[subject] == nil {
		h.subscribers[subject] = make(map[int]*sseSubscriber)
	}
	h.subscribers[subject][sub.id] = sub
	return sub
}

func (h *sseHub) unsubscribe(subject string, sub *sseSubscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if m := h.subscribers[subject]; m != nil {
		delete(m, sub.id)
		if len(m) == 0 {
			delete(h.subscribers, subject)
		}
	}
}

// broadcast delivers an event to all subscribers of a subject.
// Slow subscribers (full channel) are dropped — streaming chunks are ephemeral.
func (h *sseHub) broadcast(subject string, ev stream.ChunkEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, sub := range h.subscribers[subject] {
		select {
		case sub.ch <- ev:
		default:
		}
	}
}

// writeSSEEvent writes a single SSE event and flushes.
func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, id any, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\nid: %v\ndata: %s\n\n", eventType, id, b); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// sseWriter serializes concurrent writes to a streaming response so the
// keep-alive goroutine cannot interleave with event writes.
type sseWriter struct {
	mu sync.Mutex
	w  http.ResponseWriter
	f  http.Flusher
}

func (s *sseWriter) event(eventType string, id any, data any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return writeSSEEvent(s.w, s.f, eventType, id, data)
}

func (s *sseWriter) comment() (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() {
		// A Flush on a response whose connection was just torn down by the
		// client can panic inside net/http. Recover it into an error so the
		// caller stops the keep-alive goroutine instead of crashing the process.
		if r := recover(); r != nil {
			err = fmt.Errorf("sse flush panic: %v", r)
		}
	}()
	if _, err := s.w.Write([]byte(": keepalive\n\n")); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// keepSSEAlive periodically writes comment lines so intermediate proxies do
// not drop long-idle streaming connections. It must never take down the
// process: the connection can disappear at any moment, so any panic is
// recovered and the goroutine exits.
func keepSSEAlive(ctx context.Context, sw *sseWriter) {
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("keepSSEAlive recovered from panic", "panic", r)
		}
	}()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := sw.comment(); err != nil {
				return
			}
		}
	}
}

// restOutboundStream opens an SSE stream of outbound chunk/complete events for a channel.
// Supports resumability via the Last-Event-ID header and ring buffer replay.
func (s *Server) restOutboundStream(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		http.Error(w, "channel query param is required", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// Refuse before any bytes are written: once the SSE headers and the opening
	// comment are out, an error answer is just a 200 with a strange body.
	chatID := r.URL.Query().Get("chat_id")
	if chatID != "" && s.streams == nil {
		http.Error(w, "conversation streaming is not available", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send an initial comment so the client receives headers immediately.
	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	flusher.Flush()
	slog.Debug("outbound stream opened", "channel", channel, "chat_id", r.URL.Query().Get("chat_id"))

	// A conversation-scoped subscription reads the durable log: replay and live
	// delivery are the same query, and a second bus replica can serve it.
	if chatID != "" {
		sw := &sseWriter{w: w, f: flusher}
		go keepSSEAlive(r.Context(), sw)
		s.streamConversation(r.Context(), sw, r, channel, chatID, r.Header.Get("Last-Event-ID"))
		return
	}

	lastID, _ := strconv.Atoi(r.Header.Get("Last-Event-ID"))
	// The IDs belong to *this process*: the ring's counter starts at 1 every time
	// the bus starts. A client's resume point does not — it outlives us, so after
	// a restart the gateway (or a browser tab) still asks for, say, 20000 while we
	// are handing out 1, 2, 3. Every event would then be dropped by the
	// `ID <= lastSent` guard below, silently and for good: the client never sees a
	// chunk again while it looks perfectly connected. A resume point past our
	// newest event was therefore not issued here — start from the beginning of
	// what this process has.
	if newest := s.chunks.Channel(channel).Newest(); lastID > newest {
		slog.Warn("outbound stream asked to resume past this process's newest event; starting fresh",
			"channel", channel, "last_event_id", lastID, "newest", newest)
		lastID = 0
	}

	sw := &sseWriter{w: w, f: flusher}
	sub := s.sse.subscribe(channel)
	defer s.sse.unsubscribe(channel, sub)

	// Replay events after the last delivered ID, then drain live events.
	lastSent := lastID
	replay := s.chunks.Channel(channel).EventsFrom(lastID)
	for _, ev := range replay {
		if err := sw.event(ev.Type, ev.ID, ev); err != nil {
			return
		}
		lastSent = ev.ID
	}

	go keepSSEAlive(r.Context(), sw)

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-sub.ch:
			if ev.ID <= lastSent {
				continue
			}
			if err := sw.event(ev.Type, ev.ID, ev); err != nil {
				return
			}
			lastSent = ev.ID
		}
	}
}

// restOutboundCursor answers the conversation's newest event id. A gateway that
// is about to dispatch a message subscribes from here, so it receives the reply
// without replaying the previous one.
func (s *Server) restOutboundCursor(w http.ResponseWriter, r *http.Request) {
	channel := r.URL.Query().Get("channel")
	chatID := r.URL.Query().Get("chat_id")
	if channel == "" || chatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel and chat_id are required"})
		return
	}
	if s.streams == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "conversation streaming is not available"})
		return
	}
	id, err := s.streams.Newest(r.Context(), channel, chatID)
	if err != nil {
		slog.Error("stream: read cursor", "channel", channel, "chat_id", chatID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// restOutboundChunk accepts a streaming chunk from an agent and broadcasts it.
func (s *Server) restOutboundChunk(w http.ResponseWriter, r *http.Request) {
	var chunk nanoprotocol.OutboundChunk
	if err := json.NewDecoder(r.Body).Decode(&chunk); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	channel := chunk.Channel
	if channel == "" || chunk.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel and chat_id are required"})
		return
	}

	ev := stream.ChunkEvent{
		Type:      "chunk",
		Kind:      chunk.Kind,
		MessageID: chunk.MessageID,
		Seq:       chunk.Seq,
		Content:   chunk.Content,
		AgentID:   chunk.AgentID,
		UserID:    chunk.UserID,
		Channel:   chunk.Channel,
		ChatID:    chunk.ChatID,
		SessionID: chunk.SessionID,
	}
	ev.ID = s.chunks.Channel(channel).Append(ev)
	s.recordAndBroadcast(r.Context(), ev)

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// restOutboundComplete accepts the final assembled message for a streaming turn.
func (s *Server) restOutboundComplete(w http.ResponseWriter, r *http.Request) {
	var complete nanoprotocol.OutboundComplete
	if err := json.NewDecoder(r.Body).Decode(&complete); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	channel := complete.Channel
	if channel == "" || complete.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel and chat_id are required"})
		return
	}

	ev := stream.ChunkEvent{
		Type:        "complete",
		MessageID:   complete.MessageID,
		FullContent: complete.FullContent,
		Summary:     complete.Summary,
		Error:       complete.Error,
		ErrorDetail: complete.ErrorDetail,
		AgentID:     complete.AgentID,
		UserID:      complete.UserID,
		Channel:     complete.Channel,
		ChatID:      complete.ChatID,
		SessionID:   complete.SessionID,
	}
	ev.ID = s.chunks.Channel(channel).Append(ev)
	s.recordAndBroadcast(r.Context(), ev)

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// broadcastOutbound delivers a queued outbound message to SSE subscribers as a
// complete event. This keeps non-streaming agents compatible with SSE gateways.
func (s *Server) broadcastOutbound(msg nanoprotocol.OutboundMessage) {
	if msg.Channel == "" {
		return
	}
	ev := stream.ChunkEvent{
		Type:        "complete",
		MessageID:   msg.MessageID,
		FullContent: msg.Content,
		AgentID:     msg.AgentID,
		UserID:      msg.UserID,
		Channel:     msg.Channel,
		ChatID:      msg.ChatID,
		SessionID:   msg.SessionID,
	}
	ev.ID = s.chunks.Channel(msg.Channel).Append(ev)
	s.recordAndBroadcast(context.Background(), ev)

	// Delivery for streaming channels is the SSE broadcast itself, so resolve
	// the queue row to 'done' rather than leaving it pending forever.
	if msg.MessageID != "" {
		if err := s.store.MarkOutboundDone(msg.MessageID); err != nil {
			slog.Warn("mark outbound done failed", "message_id", msg.MessageID, "error", err)
		}
	}
}

// restEventsStream opens the platform change stream (config, capabilities,
// agents, models). Replay comes from the durable log, live delivery from this
// replica's tailer: one reader per replica, not one per subscriber.
func (s *Server) restEventsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if _, err := w.Write([]byte(": connected\n\n")); err != nil {
		return
	}
	flusher.Flush()

	sw := &sseWriter{w: w, f: flusher}
	go keepSSEAlive(r.Context(), sw)

	// Without a database (tests, degraded start) only the local hub exists.
	if s.platform == nil {
		sub := s.events.subscribe("events")
		defer s.events.unsubscribe("events", sub)
		for {
			select {
			case <-r.Context().Done():
				return
			case ev := <-sub.ch:
				if err := sw.event(ev.Type, "", ev); err != nil {
					return
				}
			}
		}
	}

	lastID := r.Header.Get("Last-Event-ID")
	if lastID != "" {
		if _, err := bson.ObjectIDFromHex(lastID); err != nil {
			lastID = ""
		}
	}
	// A fresh subscriber starts at the current end: replaying ten minutes of
	// notifications into every agent start would only trigger re-reads it is
	// already doing.
	if lastID == "" {
		if newest, err := s.platform.Newest(r.Context()); err == nil {
			lastID = newest
		}
	}

	metrics.StreamSubscribers.Inc()
	defer metrics.StreamSubscribers.Dec()

	if s.platformTailer == nil {
		s.eventsStreamPolling(r.Context(), sw, lastID)
		return
	}

	// Subscribe before replaying (see streamConversation).
	sub := s.platformTailer.Subscribe("")
	defer sub.Close()

	if err := s.replayPlatformEvents(r.Context(), sw, &lastID); err != nil {
		return
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.Events():
			if !ok {
				return
			}
			if err := s.sendPlatformEvent(sw, ev, &lastID); err != nil {
				return
			}
		case <-sub.Lag():
			if err := s.replayPlatformEvents(r.Context(), sw, &lastID); err != nil {
				return
			}
		}
	}
}

// sendPlatformEvent writes one notification unless the client already has it.
func (s *Server) sendPlatformEvent(sw *sseWriter, ev stream.PlatformEvent, lastID *string) error {
	hex := ev.ID.Hex()
	if hex == "" || hex <= *lastID {
		return nil
	}
	out := stream.ChunkEvent{Type: ev.Type, Content: ev.Content}
	if err := sw.event(out.Type, hex, out); err != nil {
		return err
	}
	*lastID = hex
	return nil
}

func (s *Server) replayPlatformEvents(ctx context.Context, sw *sseWriter, lastID *string) error {
	for {
		events, err := s.platform.Since(ctx, *lastID, 500)
		if err != nil {
			slog.Warn("platform events: read failed", "error", err)
			return nil
		}
		for _, ev := range events {
			if err := s.sendPlatformEvent(sw, ev, lastID); err != nil {
				return err
			}
		}
		if len(events) < 500 {
			return nil
		}
	}
}

// eventsStreamPolling is the no-tailer path.
func (s *Server) eventsStreamPolling(ctx context.Context, sw *sseWriter, lastID string) {
	sub := s.events.subscribe("events")
	defer s.events.unsubscribe("events", sub)

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		events, err := s.platform.Since(ctx, lastID, 500)
		if err != nil {
			slog.Warn("platform events: read failed", "error", err)
		}
		if err := s.sendPlatformEventList(sw, events, &lastID); err != nil {
			return
		}
		if len(events) > 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-sub.ch:
		case <-ticker.C:
		}
	}
}

func (s *Server) sendPlatformEventList(sw *sseWriter, events []stream.PlatformEvent, lastID *string) error {
	for _, ev := range events {
		if err := s.sendPlatformEvent(sw, ev, lastID); err != nil {
			return err
		}
	}
	return nil
}

// emitConfigChanged broadcasts a config_changed notification for the key.
// Keys use the agent.<id>.<setting> or agent.default.<setting> pattern.
func (s *Server) emitConfigChanged(key string) {
	if !strings.HasPrefix(key, "agent.") {
		return
	}
	rest := strings.TrimPrefix(key, "agent.")
	parts := strings.SplitN(rest, ".", 2)
	agentID := parts[0]
	keys := []string{key}
	if agentID == "default" {
		// a default change affects every agent; signal all
		agentID = ""
	}

	notification := nanoprotocol.ConfigChangedNotification{
		AgentID: agentID,
		Keys:    keys,
	}
	s.recordPlatformEvent(nanoprotocol.TypeConfigChanged, marshalOrEmpty(notification))
}

// emitCapabilitiesChanged broadcasts a notification so every connected agent
// re-pulls the capability catalog (scope-independent; changes affect all).
func (s *Server) emitCapabilitiesChanged(kind ...string) {
	keys := []string{"capabilities"}
	if len(kind) > 0 {
		keys = append(keys, "capabilities."+kind[0])
	}
	notification := nanoprotocol.ConfigChangedNotification{
		AgentID: "",
		Keys:    keys,
	}
	s.recordPlatformEvent(nanoprotocol.TypeConfigChanged, marshalOrEmpty(notification))
}

// emitAgentsChanged broadcasts an agents_changed notification for a user so
// supervisors (hypervisors) can react to agent create/update/delete without a
// container restart.
func (s *Server) emitAgentsChanged(userID, action, agentID string) {
	notification := nanoprotocol.AgentChangeNotification{
		UserID:  userID,
		Action:  action,
		AgentID: agentID,
	}
	s.recordPlatformEvent(nanoprotocol.TypeAgentsChanged, marshalOrEmpty(notification))
}

// emitModelsChanged broadcasts a models catalog change. It rides the same
// config_changed stream so agents hot-reload their model/provider definitions;
// supervisors deliberately do NOT restart agents on config_changed.
func (s *Server) emitModelsChanged(ownerUserID string) {
	agentID := ""
	if ownerUserID != "" {
		agentID = ownerUserID // informational; agents filter by owner themselves
	}
	notification := nanoprotocol.ConfigChangedNotification{
		AgentID: agentID,
		Keys:    []string{"models"},
	}
	s.recordPlatformEvent(nanoprotocol.TypeConfigChanged, marshalOrEmpty(notification))
}

// emitAgentSettingsChanged tells a specific running agent to re-pull its
// effective settings (AgentType baseline + per-agent overrides) and rebuild.
// It rides the config_changed stream; the "capabilities." prefix routes it
// through the idle-deferred rebuild path so tools/prompt/settings all refresh.
func (s *Server) emitAgentSettingsChanged(agentID string) {
	if agentID == "" {
		return
	}
	notification := nanoprotocol.ConfigChangedNotification{
		AgentID: agentID,
		Keys:    []string{"capabilities.agent_settings"},
	}
	s.recordPlatformEvent(nanoprotocol.TypeConfigChanged, marshalOrEmpty(notification))
}

func marshalOrEmpty(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Warn("marshal event payload", "error", err)
		return ""
	}
	return string(b)
}
