package server

import (
	"log/slog"
	"net/http"
	"sort"

	"gitee.com/kobeyoung81/nanobus/internal/promptbundle"
)

// The prompt baseline of the built-in types, as the admin page needs it.
//
// The agent-types listing already carries the prompt text; what it cannot carry
// is whether that text is the bundled one, an older seed or an admin edit —
// only the bundle in this binary knows. These two endpoints answer that, and let
// an operator put one prompt back without reaching for `prompts seed --force`
// (which would overwrite edits on every type).

type agentTypePromptStatus struct {
	ID             string `json:"id"`
	Source         string `json:"source"`
	StoredVersion  int    `json:"stored_version"`
	BundledVersion int    `json:"bundled_version"`
}

type agentTypePromptStatusResponse struct {
	Items []agentTypePromptStatus `json:"items"`
}

// bundledPrompts indexes the embedded prompts by agent-type id.
func bundledPrompts() (map[string]promptbundle.BundledPrompt, error) {
	bundled, err := promptbundle.Load()
	if err != nil {
		return nil, err
	}
	out := make(map[string]promptbundle.BundledPrompt, len(bundled))
	for _, p := range bundled {
		out[p.ID] = p
	}
	return out, nil
}

// restAgentTypePromptStatus classifies every type's stored prompt.
func (s *Server) restAgentTypePromptStatus(w http.ResponseWriter, r *http.Request) {
	states, err := s.store.ListPromptStates()
	if err != nil {
		slog.Error("list prompt states", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	bundled, err := bundledPrompts()
	if err != nil {
		slog.Error("load prompt bundle", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	items := make([]agentTypePromptStatus, 0, len(states))
	for id, state := range states {
		entry, ok := bundled[id]
		items = append(items, agentTypePromptStatus{
			ID:             id,
			Source:         string(promptbundle.Classify(state.Text, state.Hash, entry, ok)),
			StoredVersion:  state.Version,
			BundledVersion: entry.Version,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	writeJSON(w, http.StatusOK, agentTypePromptStatusResponse{Items: items})
}

// restResetAgentTypePrompt restores one type's prompt from the bundle, edits
// included. The type itself must already exist: a reset is not a create.
func (s *Server) restResetAgentTypePrompt(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	bundled, err := bundledPrompts()
	if err != nil {
		slog.Error("load prompt bundle", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	entry, ok := bundled[id]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this type ships no bundled prompt"})
		return
	}
	if err := s.store.ResetTypePrompt(id, entry.Text, entry.Version); err != nil {
		slog.Error("reset agent type prompt", "id", id, "error", err)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	// The snapshot carries the prompt, so a running agent only picks the new text
	// up through a capability reload.
	s.emitCapabilitiesChanged("agent_type")
	s.recordAudit(r, "agent_type.prompt.reset", "agent_type", id, map[string]any{
		"version": entry.Version,
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "reset"})
}
