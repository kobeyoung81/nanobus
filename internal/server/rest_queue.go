package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// restPollInbound long-polls for a pending inbound message.
// Nanosec controls its own consumption rate; the poll atomically claims a
// message (returns 200 with the message) or returns 204 when the queue is empty.
func (s *Server) restPollInbound(w http.ResponseWriter, r *http.Request) {
	var req nanoprotocol.PollInboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	msg, err := s.store.ClaimInbound(req.UserID, req.BindingID, req.AgentID)
	if err != nil {
		slog.Error("poll inbound", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if msg == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	writeJSON(w, http.StatusOK, msg)
}

// restPostOutbound posts an outbound message to the bus.
func (s *Server) restPostOutbound(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.maxContentBytes)+maxBodySlack)
	var msg nanoprotocol.OutboundMessage
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		if writeBodyTooLarge(w, err) {
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := s.checkContent(msg.Content); err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": err.Error()})
		return
	}

	if err := s.store.PostOutbound(msg); err != nil {
		slog.Error("post outbound", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	s.broadcastOutbound(msg)

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// restRPC handles the nanoprotocol message envelope over HTTP.
// It is the transport used by nanoprotocol.Client after the WebSocket migration.
func (s *Server) restRPC(w http.ResponseWriter, r *http.Request) {
	var msg nanoprotocol.Message
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	resp := s.handleMessage(msg)
	if resp == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	writeJSON(w, http.StatusOK, resp)
}
