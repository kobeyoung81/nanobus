package server

import (
	"log/slog"
	"net/http"
	"time"
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

// restConfigUpdatedAt answers "when did this component's configuration last
// change", which the console shows per component.
func (s *Server) restConfigUpdatedAt(w http.ResponseWriter, r *http.Request) {
	component := r.PathValue("component")
	updated, err := s.store.ConfigUpdatedAt(component)
	if err != nil {
		slog.Error("config updated at", "component", component, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	body := map[string]any{"component_type": component, "updated_at": ""}
	if !updated.IsZero() {
		body["updated_at"] = updated.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, body)
}

// restCountConfigItems counts a component's items by key pattern and value.
//
// It exists for one question — "how many channels are enabled" — which is a
// pattern over keys (`channels.*.enabled`) plus a value filter. Answering it here
// keeps the pattern out of the console, where it used to be a SQL LIKE.
func (s *Server) restCountConfigItems(w http.ResponseWriter, r *http.Request) {
	component := r.PathValue("component")
	query := r.URL.Query()
	count, err := s.store.CountConfigItems(component,
		query.Get("key_prefix"), query.Get("key_suffix"), query.Get("value"))
	if err != nil {
		slog.Error("count config items", "component", component, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"component_type": component, "count": count})
}
