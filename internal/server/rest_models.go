package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/llmprobe"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// ======== model catalog REST ========
//
// Bus routes are unauthenticated by design (internal network); the portal
// enforces admin (operator+) vs user ownership. Admin routes below operate on
// 'platform' scope, user routes on the caller's own 'user' scope.

type providerBody struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

type modelBody struct {
	ProviderID      int64  `json:"provider_id"`
	Name            string `json:"name"`
	Model           string `json:"model"`
	ContextWindow   int    `json:"context_window"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	ThinkingEnabled bool   `json:"thinking_enabled"`
	ThinkingEffort  string `json:"thinking_effort"`
	Params          string `json:"params"`
}

func maskAPIKey(k string) string {
	if k == "" {
		return ""
	}
	return maskValue
}

func maskProvider(p nanoprotocol.ModelProviderDef) nanoprotocol.ModelProviderDef {
	p.APIKey = maskAPIKey(p.APIKey)
	return p
}

// listCatalog writes {providers, models} for a scope filter (global only, or
// global + owner's user rows).
func (s *Server) listCatalog(w http.ResponseWriter, ownerUserID string) {
	providers, err := s.store.ListModelProviders(ownerUserID)
	if err != nil {
		slog.Error("list providers", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	models, err := s.store.ListModels(ownerUserID)
	if err != nil {
		slog.Error("list models", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if ownerUserID != "" {
		for i := range providers {
			providers[i] = maskProvider(providers[i])
		}
	}
	defaultModelID := s.store.DefaultModelForType(s.store.DefaultAgentTypeID())
	writeJSON(w, http.StatusOK, map[string]any{
		"providers":        providers,
		"models":           models,
		"default_model_id": defaultModelID,
	})
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request, scope, owner string) {
	var body providerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Name == "" || body.BaseURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and base_url are required"})
		return
	}
	id, err := s.store.CreateModelProvider(nanoprotocol.ModelProviderDef{
		Scope: scope, OwnerUserID: owner, Name: body.Name, Kind: body.Kind, BaseURL: body.BaseURL, APIKey: body.APIKey,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emitModelsChanged(owner)
	s.recordAudit(r, "models.provider.create", "model_provider", strconv.FormatInt(id, 10), map[string]any{"scope": scope})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "created"})
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request, owner string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body providerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := s.store.UpdateModelProvider(id, owner, nanoprotocol.ModelProviderDef{
		Name: body.Name, Kind: body.Kind, BaseURL: body.BaseURL, APIKey: body.APIKey,
	}); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider not found"})
		return
	}
	s.emitModelsChanged(owner)
	s.recordAudit(r, "models.provider.update", "model_provider", r.PathValue("id"), nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": "updated"})
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request, owner string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := s.store.DeleteModelProvider(id, owner); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider not found"})
		return
	}
	s.emitModelsChanged(owner)
	s.recordAudit(r, "models.provider.delete", "model_provider", r.PathValue("id"), nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": "deleted"})
}

func (s *Server) createModel(w http.ResponseWriter, r *http.Request, scope, owner string) {
	var body modelBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.ProviderID == 0 || body.Name == "" || body.Model == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider_id, name and model are required"})
		return
	}
	if !store.ValidParamsJSON(body.Params) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "params must be a JSON object"})
		return
	}
	prov, err := s.store.GetModelProvider(body.ProviderID)
	if err != nil || prov == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider not found"})
		return
	}
	if owner != "" && prov.Scope == "user" && prov.OwnerUserID != owner {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "provider not owned"})
		return
	}
	id, err := s.store.CreateModel(nanoprotocol.ModelDef{
		ProviderID: body.ProviderID, Scope: scope, OwnerUserID: owner, Name: body.Name, Model: body.Model,
		ContextWindow: body.ContextWindow, MaxOutputTokens: body.MaxOutputTokens,
		ThinkingEnabled: body.ThinkingEnabled, ThinkingEffort: body.ThinkingEffort, Params: body.Params,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emitModelsChanged(owner)
	s.recordAudit(r, "models.model.create", "model", strconv.FormatInt(id, 10), map[string]any{"scope": scope})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "status": "created"})
}

func (s *Server) updateModel(w http.ResponseWriter, r *http.Request, owner string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body modelBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if !store.ValidParamsJSON(body.Params) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "params must be a JSON object"})
		return
	}
	if err := s.store.UpdateModel(id, owner, nanoprotocol.ModelDef{
		Name: body.Name, Model: body.Model, ContextWindow: body.ContextWindow, MaxOutputTokens: body.MaxOutputTokens,
		ThinkingEnabled: body.ThinkingEnabled, ThinkingEffort: body.ThinkingEffort, Params: body.Params,
	}); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}
	s.emitModelsChanged(owner)
	s.recordAudit(r, "models.model.update", "model", r.PathValue("id"), nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": "updated"})
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request, owner string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	if err := s.store.DeleteModel(id, owner); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	}
	s.emitModelsChanged(owner)
	s.recordAudit(r, "models.model.delete", "model", r.PathValue("id"), nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": "deleted"})
}

// restApplyModelToBuiltInTypes makes one platform model available to every
// seeded agent type. It exists because a fresh installation has no per-type
// model bindings at all: adding a model to the catalog used to leave every
// agent without one, and the agent then restarted in a loop.
func (s *Server) restApplyModelToBuiltInTypes(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	applied, already, defaults, err := s.store.ApplyModelToBuiltInTypes(id)
	switch {
	case errors.Is(err, store.ErrModelNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
		return
	case errors.Is(err, store.ErrNotPlatformModel):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "only platform models can be applied to built-in agent types"})
		return
	case err != nil:
		slog.Error("apply model to agent types", "model", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.emitModelsChanged("")
	s.emitCapabilitiesChanged("agent_type")
	s.recordAudit(r, "models.model.apply_to_agent_types", "model", strconv.FormatInt(id, 10), map[string]any{
		"applied": len(applied), "already": len(already), "defaults": len(defaults),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"model_id": id, "applied": applied, "already": already, "defaults": defaults,
	})
}

// ======== admin (global) route handlers ========

func (s *Server) restListModelsAdmin(w http.ResponseWriter, r *http.Request) { s.listCatalog(w, "") }
func (s *Server) restCreateProviderAdmin(w http.ResponseWriter, r *http.Request) {
	s.createProvider(w, r, "platform", "")
}
func (s *Server) restUpdateProviderAdmin(w http.ResponseWriter, r *http.Request) {
	s.updateProvider(w, r, "")
}
func (s *Server) restDeleteProviderAdmin(w http.ResponseWriter, r *http.Request) {
	s.deleteProvider(w, r, "")
}
func (s *Server) restCreateModelAdmin(w http.ResponseWriter, r *http.Request) {
	s.createModel(w, r, "platform", "")
}
func (s *Server) restUpdateModelAdmin(w http.ResponseWriter, r *http.Request) {
	s.updateModel(w, r, "")
}
func (s *Server) restDeleteModelAdmin(w http.ResponseWriter, r *http.Request) {
	s.deleteModel(w, r, "")
}

// ======== user (owner-scoped) route handlers ========

func (s *Server) restListMyModels(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.listCatalog(w, userID)
}
func (s *Server) restCreateMyProvider(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.createProvider(w, r, "user", userID)
}
func (s *Server) restUpdateMyProvider(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.updateProvider(w, r, userID)
}
func (s *Server) restDeleteMyProvider(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.deleteProvider(w, r, userID)
}
func (s *Server) restCreateMyModel(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.createModel(w, r, "user", userID)
}
func (s *Server) restUpdateMyModel(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.updateModel(w, r, userID)
}
func (s *Server) restDeleteMyModel(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.deleteModel(w, r, userID)
}

// ======== connection tests ========
//
// Test endpoints probe the configured provider/model and always respond 200
// with {ok:false,error} on probe failure so the UI can render the reason.

const testProbeTimeout = 20 * time.Second

type providerTestBody struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

type modelTestBody struct {
	ID         int64  `json:"id"`
	ProviderID int64  `json:"provider_id"`
	Model      string `json:"model"`
}

func providerVisible(p *nanoprotocol.ModelProviderDef, owner string) bool {
	if p.Scope == "platform" {
		return true
	}
	return owner != "" && p.OwnerUserID == owner
}

func modelVisible(m *nanoprotocol.ModelDef, owner string) bool {
	if m.Scope == "platform" {
		return true
	}
	return owner != "" && m.OwnerUserID == owner
}

func (s *Server) testProvider(w http.ResponseWriter, r *http.Request, owner string) {
	var body providerTestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	kind, baseURL, apiKey := body.Kind, body.BaseURL, body.APIKey
	if body.ID != 0 {
		p, err := s.store.GetModelProvider(body.ID)
		if err != nil || p == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "provider not found"})
			return
		}
		if !providerVisible(p, owner) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "provider not owned"})
			return
		}
		kind, baseURL, apiKey = p.Kind, p.BaseURL, p.APIKey
	}
	if body.Kind != "" {
		kind = body.Kind
	}
	if body.BaseURL != "" {
		baseURL = body.BaseURL
	}
	if body.APIKey != "" && body.APIKey != maskValue {
		apiKey = body.APIKey
	}

	ctx, cancel := context.WithTimeout(r.Context(), testProbeTimeout)
	defer cancel()
	start := time.Now()
	models, err := llmprobe.ProbeProvider(ctx, kind, baseURL, apiKey)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "latency_ms": latency, "error": err.Error()})
		return
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	s.recordAudit(r, "models.provider.test", "model_provider", fmt.Sprint(body.ID), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"latency_ms":    latency,
		"models":        ids,
		"model_details": models,
	})
}

func (s *Server) testModel(w http.ResponseWriter, r *http.Request, owner string) {
	var body modelTestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	providerID := body.ProviderID
	model := body.Model
	if body.ID != 0 {
		m, err := s.store.GetModel(body.ID)
		if err != nil || m == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
			return
		}
		if !modelVisible(m, owner) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "model not owned"})
			return
		}
		providerID = m.ProviderID
		model = m.Model
	}
	if providerID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider_id is required"})
		return
	}
	p, err := s.store.GetModelProvider(providerID)
	if err != nil || p == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider not found"})
		return
	}
	if owner != "" && p.Scope == "user" && p.OwnerUserID != owner {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "provider not owned"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), testProbeTimeout)
	defer cancel()
	start := time.Now()
	reply, err := llmprobe.ProbeModel(ctx, p.Kind, p.BaseURL, p.APIKey, model)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "latency_ms": latency, "error": err.Error()})
		return
	}
	s.recordAudit(r, "models.model.test", "model", fmt.Sprint(body.ID), nil)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "latency_ms": latency, "reply": reply})
}

func (s *Server) restTestProviderAdmin(w http.ResponseWriter, r *http.Request) {
	s.testProvider(w, r, "")
}
func (s *Server) restTestModelAdmin(w http.ResponseWriter, r *http.Request) {
	s.testModel(w, r, "")
}
func (s *Server) restTestProviderMy(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.testProvider(w, r, userID)
}
func (s *Server) restTestModelMy(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.testModel(w, r, userID)
}
