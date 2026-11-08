package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// ======== admin: agent types ========

func (s *Server) restListAgentTypes(w http.ResponseWriter, r *http.Request) {
	// Admin listing: every definition, always — session agents, subagents and the
	// utility helpers. The query string is deliberately ignored here; the user
	// catalog (restListMyAgentTypes) is the narrow one.
	types, err := s.store.ListAgentTypes(false)
	if err != nil {
		slog.Error("list agent types", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, nanoprotocol.AgentTypesResponse{Types: types})
}

func (s *Server) restGetAgentType(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetAgentType(r.PathValue("id"))
	if err != nil {
		slog.Error("get agent type", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if t == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent type not found"})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) restSaveAgentType(w http.ResponseWriter, r *http.Request) {
	var def nanoprotocol.AgentTypeDef
	if err := json.NewDecoder(r.Body).Decode(&def); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if def.ID == "" {
		def.ID = r.PathValue("id")
	}
	if def.ID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}
	if err := s.store.SaveAgentType(def); err != nil {
		slog.Error("save agent type", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emitCapabilitiesChanged("agent_type")
	s.recordAudit(r, "agent_type.save", "agent_type", def.ID, nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": def.ID, "status": "saved"})
}

// restSetTypeCapability upserts one per-type capability baseline
// (default_enabled + locked) for builtin tools, MCP servers and skills.
func (s *Server) restSetTypeCapability(w http.ResponseWriter, r *http.Request) {
	typeID := r.PathValue("id")
	var body struct {
		Kind           string `json:"kind"`
		Ref            string `json:"ref"`
		DefaultEnabled bool   `json:"default_enabled"`
		Locked         string `json:"locked"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Kind == "" || body.Ref == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind and ref are required"})
		return
	}
	if body.Locked != "" && body.Locked != "on" && body.Locked != "off" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "locked must be '', 'on' or 'off'"})
		return
	}
	if err := s.store.SetTypeCapabilityState(typeID, body.Kind, body.Ref, body.DefaultEnabled, body.Locked); err != nil {
		slog.Error("set type capability state", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emitCapabilitiesChanged("agent_type")
	s.recordAudit(r, "agent_type.capability", "agent_type", typeID, map[string]any{
		"kind": body.Kind, "ref": body.Ref,
		"default_enabled": body.DefaultEnabled, "locked": body.Locked,
	})
	writeJSON(w, http.StatusOK, map[string]string{"id": typeID, "status": "updated"})
}

// restDeleteTypeCapability removes a per-type capability baseline row so the
// type inherits the definition-plane (global) default again.
func (s *Server) restDeleteTypeCapability(w http.ResponseWriter, r *http.Request) {
	typeID := r.PathValue("id")
	kind := r.URL.Query().Get("kind")
	ref := r.URL.Query().Get("ref")
	if kind == "" || ref == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind and ref are required"})
		return
	}
	if err := s.store.DeleteTypeCapabilityState(typeID, kind, ref); err != nil {
		slog.Error("delete type capability", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.emitCapabilitiesChanged("agent_type")
	s.recordAudit(r, "agent_type.capability.delete", "agent_type", typeID, map[string]any{"kind": kind, "ref": ref})
	writeJSON(w, http.StatusOK, map[string]string{"id": typeID, "status": "deleted"})
}

func (s *Server) restDeleteAgentType(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteAgentType(id); err != nil {
		slog.Error("delete agent type", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emitCapabilitiesChanged("agent_type")
	s.recordAudit(r, "agent_type.delete", "agent_type", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}

// ======== admin: permission keys ========

func (s *Server) restListPermissionKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListPermissionKeys()
	if err != nil {
		slog.Error("list permission keys", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, nanoprotocol.PermissionKeysResponse{Keys: keys})
}

// ======== user: assignable agent types ========

// restListMyAgentTypes lists the definitions a user may configure. By default
// it returns only the assignable session agents; `?all=true` also returns the
// helper definitions (subagents/utilities) so their per-(agent,type) overrides
// can be edited.
func (s *Server) restListMyAgentTypes(w http.ResponseWriter, r *http.Request) {
	// The user catalog is session agents only. Helper definitions (subagents and
	// utilities) are platform-owned: the user config surface covers an agent and
	// its modes, never its helpers, so this list must not offer them. The query
	// string is not a way to widen it — the admin listing above is the full set.
	types, err := s.store.ListAgentTypes(true)
	if err != nil {
		slog.Error("list assignable agent types", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, nanoprotocol.AgentTypesResponse{Types: types})
}
