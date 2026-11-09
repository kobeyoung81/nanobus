package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// restListConfig returns the component types that have configuration, for the
// admin config centre's index. It exists so a caller that lists components does
// not have to read every component — or reach past this service into the
// database to ask.
func (s *Server) restListConfig(w http.ResponseWriter, r *http.Request) {
	components, err := s.store.ListConfigComponents()
	if err != nil {
		slog.Error("list config components", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"components": components})
}

// restGetConfig returns one component's configuration.
func (s *Server) restGetConfig(w http.ResponseWriter, r *http.Request) {
	component := r.PathValue("component")
	items, err := s.store.GetConfigItems(component)
	if err != nil {
		slog.Error("get config items", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, nanoprotocol.GetConfigItemsResponse{
		ComponentType: component,
		Items:         nonNilSlice(items),
	})
}

func (s *Server) restSetConfig(w http.ResponseWriter, r *http.Request) {
	component := r.PathValue("component")
	key := r.PathValue("key")

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxConfigBody))
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
		return
	}
	var body struct {
		Value string `json:"value"`
		// Optional, and used only when the item is created: a component that owns
		// a key it writes (the auth service's JWT keypair) declares its kind, and
		// `secret` is what makes the console mask it.
		ValueType   string `json:"value_type,omitempty"`
		Description string `json:"description,omitempty"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if !s.forwardConfigWrite(w, r, component, key, configWriteBody(body.Value, body.ValueType, body.Description)) {
		return
	}

	s.emitConfigChanged(key)
	s.recordAudit(r, "config.update", "config", component+"/"+key, map[string]any{
		"component": component,
		"key":       key,
	})

	writeJSON(w, http.StatusOK, configWriteResponse(component, key, body.Value))
}
