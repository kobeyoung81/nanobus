package server

import (
	"encoding/json"
	"net/http"
)

// restInternalCapabilitiesChanged broadcasts a capability-change event on behalf
// of the admin module, which owns the catalogue.
//
// Every running agent re-pulls its catalogue when it sees this. The admin module
// cannot publish it itself without holding a second connection to the bus's SSE
// hub, so it asks here — the same shape the portal uses for agent changes.
func (s *Server) restInternalCapabilitiesChanged(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Kind == "" {
		s.emitCapabilitiesChanged()
	} else {
		s.emitCapabilitiesChanged(body.Kind)
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "broadcast"})
}
