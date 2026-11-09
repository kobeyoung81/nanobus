package server

import (
	"log/slog"
	"net/http"
)

// The overview's remaining numbers: queue depths, the WeChat KF account count, and
// the two configuration figures (how many channels are enabled, when a component
// last changed). All of them were SELECTs against MySQL; all of the tables are the
// bus's now.

// restCountQueues returns the two backlogs the overview prints.
func (s *Server) restCountQueues(w http.ResponseWriter, r *http.Request) {
	inbound, outbound, err := s.store.CountPendingQueues()
	if err != nil {
		slog.Error("count queues", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"inbound_pending": inbound, "outbound_pending": outbound})
}

// restCountWeChatKF returns how many KF accounts have sync state.
func (s *Server) restCountWeChatKF(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.store.CountWeChatKFAccounts()
	if err != nil {
		slog.Error("count wechatkf accounts", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}
