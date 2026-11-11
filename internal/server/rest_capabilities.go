package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/mcpprobe"
	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// ======== shared DTOs & helpers ========

const maskValue = "***"

func maskMcp(d nanoprotocol.McpServerDef) nanoprotocol.McpServerDef {
	m := d
	if len(m.Headers) > 0 {
		headers := make(map[string]string, len(m.Headers))
		for k := range m.Headers {
			headers[k] = maskValue
		}
		m.Headers = headers
	}
	return m
}

// preserveMaskedHeaders replaces incoming masked placeholders ("***") with the
// stored secret so that editing a server whose headers were masked on read does
// not overwrite the real value.
func preserveMaskedHeaders(incoming, stored map[string]string) {
	for k, v := range incoming {
		if v != maskValue {
			continue
		}
		if old, ok := stored[k]; ok {
			incoming[k] = old
		} else {
			delete(incoming, k)
		}
	}
}

func capabilitySlug(prefix, name string) string {
	slug := strings.ToLower(name)
	var b strings.Builder
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	if b.Len() == 0 {
		b.WriteString("cap")
	}
	// Trim to keep the composite id within VARCHAR(96).
	out := b.String()
	if len(out) > 48 {
		out = out[:48]
	}
	return prefix + ":" + out
}

func randomSuffix() string {
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// ======== admin: builtin tools ========

type builtinStateBody struct {
	DefaultEnabled *bool   `json:"default_enabled"`
	Locked         *string `json:"locked"`
}

// restListBuiltin forwards to the catalogue's owner: the builtin tools moved to
// the admin module with the rest of the definition plane.
func (s *Server) restListBuiltin(w http.ResponseWriter, r *http.Request) {
	if s.admin == nil || !s.admin.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin module is not configured"})
		return
	}
	var out json.RawMessage
	if err := s.admin.GetJSON(r.Context(), "/api/v1/capabilities/builtin", &out); err != nil {
		slog.Error("list builtin tools", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "admin unavailable"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

func (s *Server) restUpdateBuiltin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if s.admin == nil || !s.admin.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin module is not configured"})
		return
	}
	status, err := s.admin.PutJSON(r.Context(), "/api/v1/capabilities/builtin/"+url.PathEscape(id), body)
	if err != nil {
		slog.Error("update builtin tool", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "admin unavailable"})
		return
	}
	if status >= http.StatusBadRequest {
		writeJSON(w, status, map[string]string{"error": "admin rejected the update"})
		return
	}
	s.emitCapabilitiesChanged("builtin")
	s.recordAudit(r, "capabilities.builtin.update", "builtin_tool", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "updated"})
}

// ======== admin: mcp servers ========

type mcpBody struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Transport      string            `json:"transport"`
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	DefaultEnabled bool              `json:"default_enabled"`
	Locked         string            `json:"locked,omitempty"`
}

func (b mcpBody) record(scope, owner string) store.McpServerRecord {
	id := b.ID
	if id == "" {
		id = capabilitySlug("mcp", b.Name) + "-" + randomSuffix()
	}
	return store.McpServerRecord{
		Capability: store.Capability{
			ID:             id,
			Scope:          scope,
			OwnerUserID:    owner,
			Name:           b.Name,
			Description:    b.Description,
			DefaultEnabled: b.DefaultEnabled,
			Locked:         b.Locked,
		},
		Transport: b.Transport,
		URL:       b.URL,
		Headers:   b.Headers,
	}
}

func (s *Server) restListMcpAdmin(w http.ResponseWriter, r *http.Request) {
	recs, err := s.store.ListMcpServers("")
	if err != nil {
		slog.Error("list mcp servers", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]nanoprotocol.McpServerDef, 0, len(recs))
	for _, rec := range recs {
		out = append(out, mcpserverDef(rec))
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out, "count": len(out)})
}

func mcpserverDef(rec store.McpServerRecord) nanoprotocol.McpServerDef {
	return nanoprotocol.McpServerDef{
		ID:             rec.ID,
		Scope:          rec.Scope,
		OwnerUserID:    rec.OwnerUserID,
		Name:           rec.Name,
		Description:    rec.Description,
		Transport:      rec.Transport,
		URL:            rec.URL,
		Headers:        rec.Headers,
		DefaultEnabled: rec.DefaultEnabled,
		Locked:         rec.Locked,
	}
}

func (s *Server) restCreateMcpAdmin(w http.ResponseWriter, r *http.Request) {
	var body mcpBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Name == "" || body.Transport == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and transport are required"})
		return
	}
	rec := body.record("platform", "")
	if err := s.store.CreateMcp(rec); err != nil {
		slog.Error("create mcp server", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emitCapabilitiesChanged("mcp")
	s.recordAudit(r, "capabilities.mcp.create", "mcp", rec.ID, nil)
	writeJSON(w, http.StatusCreated, map[string]string{"id": rec.ID, "status": "created"})
}

func (s *Server) restUpdateMcpAdmin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body mcpBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	rec := body.record("platform", "")
	rec.ID = id
	if existing, err := s.store.GetMcpServer(id); err == nil && existing != nil {
		preserveMaskedHeaders(rec.Headers, existing.Headers)
	}
	if err := s.store.UpdateMcp(id, rec); err != nil {
		slog.Error("update mcp server", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.emitCapabilitiesChanged("mcp")
	s.recordAudit(r, "capabilities.mcp.update", "mcp", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "updated"})
}

// restUpdateMcpState updates only the definition-plane default (default_enabled /
// locked) for an MCP server; used by the Capability Center's inline controls.
func (s *Server) restUpdateMcpState(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body builtinStateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if err := s.store.UpdateMcpState(id, body.DefaultEnabled, body.Locked); err != nil {
		slog.Error("update mcp state", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emitCapabilitiesChanged("mcp")
	s.recordAudit(r, "capabilities.mcp.state", "mcp", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "updated"})
}

func (s *Server) restDeleteMcpAdmin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.DeleteMcp(id, "", "platform"); err != nil {
		slog.Error("delete mcp server", "error", err)
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "mcp server not found"})
		return
	}
	s.emitCapabilitiesChanged("mcp")
	s.recordAudit(r, "capabilities.mcp.delete", "mcp", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}

// ======== MCP connection test ========

type mcpTestBody struct {
	ID        string            `json:"id"`
	Transport string            `json:"transport"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers"`
}

func mcpVisible(rec *store.McpServerRecord, owner string) bool {
	if rec.Scope == "platform" {
		return true
	}
	return owner != "" && rec.OwnerUserID == owner
}

// testMcp probes an MCP server (by id, or an unsaved draft) and always responds
// 200 with {ok:false,error} on failure. owner=="" is the admin/global scope.
func (s *Server) testMcp(w http.ResponseWriter, r *http.Request, owner string) {
	var body mcpTestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	transport, rawURL := body.Transport, body.URL
	headers := map[string]string{}
	if body.ID != "" {
		rec, err := s.store.GetMcpServer(body.ID)
		if err != nil || rec == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "mcp server not found"})
			return
		}
		if !mcpVisible(rec, owner) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "mcp server not owned"})
			return
		}
		transport, rawURL = rec.Transport, rec.URL
		for k, v := range rec.Headers {
			headers[k] = v
		}
	}
	if body.Transport != "" {
		transport = body.Transport
	}
	if body.URL != "" {
		rawURL = body.URL
	}
	for k, v := range body.Headers {
		if v != "" && v != maskValue {
			headers[k] = v
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), testProbeTimeout)
	defer cancel()
	start := time.Now()
	result, err := mcpprobe.Probe(ctx, transport, rawURL, headers)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "latency_ms": latency, "error": err.Error()})
		return
	}
	s.recordAudit(r, "capabilities.mcp.test", "mcp", body.ID, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"latency_ms":       latency,
		"tools":            result.Tools,
		"server_name":      result.ServerName,
		"protocol_version": result.ProtocolVersion,
	})
}

func (s *Server) restTestMcpAdmin(w http.ResponseWriter, r *http.Request) {
	s.testMcp(w, r, "")
}

func (s *Server) restTestMcpMy(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	s.testMcp(w, r, userID)
}

// ======== admin: skills ========

type skillBody struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Files          map[string]string `json:"files"`
	DefaultEnabled bool              `json:"default_enabled"`
	Locked         string            `json:"locked,omitempty"`
}

func (b skillBody) record(scope, owner string) store.SkillRecord {
	id := b.ID
	if id == "" {
		id = capabilitySlug("skill", b.Name) + "-" + randomSuffix()
	}
	origin := "human"
	if scope == "platform" {
		origin = "platform"
	}
	return store.SkillRecord{
		Capability: store.Capability{
			ID:             id,
			Scope:          scope,
			OwnerUserID:    owner,
			Name:           b.Name,
			Description:    b.Description,
			DefaultEnabled: b.DefaultEnabled,
			Locked:         b.Locked,
		},
		Files:  b.Files,
		Origin: origin,
		Status: "published",
	}
}

func skillDef(rec store.SkillRecord) nanoprotocol.SkillDef {
	return nanoprotocol.SkillDef{
		ID:               rec.ID,
		Scope:            rec.Scope,
		OwnerUserID:      rec.OwnerUserID,
		Name:             rec.Name,
		Description:      rec.Description,
		Files:            rec.Files,
		DefaultEnabled:   rec.DefaultEnabled,
		Locked:           rec.Locked,
		Origin:           rec.Origin,
		Version:          rec.Version,
		Status:           rec.Status,
		PublishedVersion: rec.PublishedVersion,
		ContentHash:      rec.ContentHash,
		CreatedBy:        rec.CreatedBy,
	}
}
func (s *Server) restListSkillsAdmin(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, "GET", "/api/v1/capabilities/skills")
}
func (s *Server) restCreateSkillAdmin(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, "POST", "/api/v1/capabilities/skills")
}
func (s *Server) restUpdateSkillAdmin(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, "PUT", "/api/v1/capabilities/skills/"+url.PathEscape(r.PathValue("id")))
}
func (s *Server) restUpdateSkillState(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, "PUT", "/api/v1/capabilities/skills/"+url.PathEscape(r.PathValue("id"))+"/state")
}
func (s *Server) restDeleteSkillAdmin(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, "DELETE", "/api/v1/capabilities/skills/"+url.PathEscape(r.PathValue("id")))
}

// ======== user-scoped library ========

func userIDFromQuery(r *http.Request) string {
	return r.URL.Query().Get("user_id")
}

func (s *Server) restListMyMcp(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	recs, err := s.store.ListMcpServers(userID)
	if err != nil {
		slog.Error("list my mcp servers", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]nanoprotocol.McpServerDef, 0, len(recs))
	for _, rec := range recs {
		def := maskMcp(mcpserverDef(rec))
		out = append(out, def)
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out, "count": len(out)})
}

func (s *Server) restCreateMyMcp(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	var body mcpBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Name == "" || body.Transport == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and transport are required"})
		return
	}
	rec := body.record("user", userID)
	if err := s.store.CreateMcp(rec); err != nil {
		slog.Error("create my mcp server", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.emitCapabilitiesChanged("mcp")
	s.recordAudit(r, "capabilities.mcp.create", "mcp", rec.ID, map[string]any{"user_id": userID})
	writeJSON(w, http.StatusCreated, map[string]string{"id": rec.ID, "status": "created"})
}

func (s *Server) restUpdateMyMcp(w http.ResponseWriter, r *http.Request) {
	userID, id := userIDFromQuery(r), r.PathValue("id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	rec, err := s.store.GetMcpServer(id)
	if err != nil || rec == nil || rec.Scope != "user" || rec.OwnerUserID != userID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "mcp server not found"})
		return
	}
	var body mcpBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	upd := body.record("user", userID)
	upd.ID = id
	preserveMaskedHeaders(upd.Headers, rec.Headers)
	if err := s.store.UpdateMcp(id, upd); err != nil {
		slog.Error("update my mcp server", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.emitCapabilitiesChanged("mcp")
	s.recordAudit(r, "capabilities.mcp.update", "mcp", id, map[string]any{"user_id": userID})
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "updated"})
}

func (s *Server) restDeleteMyMcp(w http.ResponseWriter, r *http.Request) {
	userID, id := userIDFromQuery(r), r.PathValue("id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if err := s.store.DeleteMcp(id, userID, "user"); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "mcp server not found"})
		return
	}
	s.emitCapabilitiesChanged("mcp")
	s.recordAudit(r, "capabilities.mcp.delete", "mcp", id, map[string]any{"user_id": userID})
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}
func (s *Server) restListMySkills(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, "GET", "/api/v1/user/capabilities/library/skills?user_id="+url.QueryEscape(userIDFromQuery(r)))
}
func (s *Server) restCreateMySkill(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, "POST", "/api/v1/user/capabilities/library/skills?user_id="+url.QueryEscape(userIDFromQuery(r)))
}
func (s *Server) restUpdateMySkill(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, "PUT", "/api/v1/user/capabilities/library/skills/"+url.PathEscape(r.PathValue("id"))+"?user_id="+url.QueryEscape(userIDFromQuery(r)))
}
func (s *Server) restDeleteMySkill(w http.ResponseWriter, r *http.Request) {
	s.forwardToAdmin(w, r, "DELETE", "/api/v1/user/capabilities/library/skills/"+url.PathEscape(r.PathValue("id"))+"?user_id="+url.QueryEscape(userIDFromQuery(r)))
}

// ======== per-agent capabilities ========

type agentCapView struct {
	ID               string          `json:"id"`
	Name             string          `json:"name,omitempty"`
	Description      string          `json:"description,omitempty"`
	DefaultEnabled   bool            `json:"default_enabled"`
	Locked           string          `json:"locked"`
	Override         *bool           `json:"override"`
	Effective        bool            `json:"effective"`
	Extra            json.RawMessage `json:"extra,omitempty"`
	UpdateAvailable  bool            `json:"update_available,omitempty"`
	AckedVersion     int             `json:"acked_version,omitempty"`
	PublishedVersion int             `json:"published_version,omitempty"`
}

func (s *Server) restAgentCapabilities(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	owner, err := s.store.AgentOwner(agentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}
	if owner != userID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not the agent owner"})
		return
	}

	typeID := r.URL.Query().Get("type_id")
	if typeID == "" {
		typeID = s.store.DefaultModeTypeID(agentID)
	}
	resp, err := s.store.ResolveModeCapabilities(agentID, typeID)
	if err != nil {
		slog.Error("resolve capabilities", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	attachments := map[string]*bool{}
	for _, a := range resp.Attachments {
		enabled := a.Enabled
		attachments[string(a.Kind)+"/"+a.Ref] = &enabled
	}

	// Platform baseline comes from the AgentType, not the catalog item.
	typeBase := map[string]nanoprotocol.TypeCapabilityBinding{}
	if resp.AgentType != nil {
		for _, c := range resp.AgentType.Capabilities {
			typeBase[string(c.Kind)+"/"+c.Ref] = c
		}
	}

	// resolve applies the full precedence for one capability:
	// global lock (absolute) > type lock > per-agent/mode override >
	// type default > global (definition) default. It returns the effective
	// on/off, the per-agent override (when present and not locked), and the
	// effective default/lock to display.
	resolve := func(kind, ref string, globalDef bool, globalLock string) (effective bool, override *bool, effDefault bool, effLocked string) {
		key := kind + "/" + ref
		// The global (definition-plane) lock is admin policy: it forces
		// availability for every type and agent.
		if globalLock == "on" {
			return true, nil, globalDef, globalLock
		}
		if globalLock == "off" {
			return false, nil, globalDef, globalLock
		}
		def, locked := globalDef, ""
		if b, ok := typeBase[key]; ok {
			def, locked = b.DefaultEnabled, b.Locked
		}
		switch locked {
		case "on":
			return true, nil, def, locked
		case "off":
			return false, nil, def, locked
		}
		if o, ok := attachments[key]; ok {
			return *o, o, def, locked
		}
		return def, nil, def, locked
	}

	builtin := make([]agentCapView, 0, len(resp.BuiltinTools))
	for _, t := range resp.BuiltinTools {
		eff, ov, def, locked := resolve("builtin_tool", t.ID, t.DefaultEnabled, t.Locked)
		builtin = append(builtin, agentCapView{ID: t.ID, Description: t.Description, DefaultEnabled: def, Locked: locked, Override: ov, Effective: eff})
	}

	mcp := make([]agentCapView, 0, len(resp.McpServers))
	for _, m := range resp.McpServers {
		eff, ov, def, locked := resolve("mcp", m.ID, m.DefaultEnabled, m.Locked)
		extra, _ := json.Marshal(struct {
			Transport string `json:"transport"`
			URL       string `json:"url,omitempty"`
		}{m.Transport, m.URL})
		mcp = append(mcp, agentCapView{ID: m.ID, Name: m.Name, Description: m.Description, DefaultEnabled: def, Locked: locked, Override: ov, Effective: eff, Extra: extra})
	}

	skills := make([]agentCapView, 0, len(resp.Skills))
	for _, sk := range resp.Skills {
		eff, ov, def, locked := resolve("skill", sk.ID, sk.DefaultEnabled, sk.Locked)
		skills = append(skills, agentCapView{
			ID: sk.ID, Name: sk.Name, Description: sk.Description,
			DefaultEnabled: def, Locked: locked, Override: ov, Effective: eff,
			UpdateAvailable: sk.UpdateAvailable, AckedVersion: sk.AckedVersion,
			PublishedVersion: sk.PublishedVersion,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"agent_id":          agentID,
		"type_id":           typeID,
		"agent_type":        resp.AgentType,
		"permissions":       resp.Permissions,
		"agent_permissions": resp.AgentPermissions,
		"builtin_tools":     builtin,
		"mcp":               mcp,
		"skills":            skills,
	})
}

type agentCapBody struct {
	TypeID  string `json:"type_id,omitempty"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Enabled bool   `json:"enabled"`
}

// restSetAgentPermissions replaces an agent's instance permission overrides.
func (s *Server) restSetAgentPermissions(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	owner, err := s.store.AgentOwner(agentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}
	if owner != userID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not the agent owner"})
		return
	}
	var body struct {
		TypeID      string                        `json:"type_id"`
		Permissions []nanoprotocol.PermissionRule `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	typeID := body.TypeID
	if typeID == "" {
		typeID = r.URL.Query().Get("type_id")
	}
	if typeID == "" {
		typeID = s.store.DefaultModeTypeID(agentID)
	}
	if err := s.store.SetModePermissions(agentID, typeID, body.Permissions); err != nil {
		slog.Error("set agent permissions", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.emitCapabilitiesChanged("permissions")
	s.recordAudit(r, "capabilities.agent.permissions", "agent", agentID, map[string]any{"user_id": userID})
	writeJSON(w, http.StatusOK, map[string]string{"agent_id": agentID, "status": "updated"})
}

func (s *Server) restSetAgentCapability(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	owner, err := s.store.AgentOwner(agentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}
	if owner != userID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not the agent owner"})
		return
	}
	var body agentCapBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Kind == "" || body.Ref == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind and ref are required"})
		return
	}
	visible, err := s.store.CapabilityVisible(body.Kind, body.Ref, userID)
	if err != nil || !visible {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "capability not found or not visible"})
		return
	}
	typeID := body.TypeID
	if typeID == "" {
		typeID = s.store.DefaultModeTypeID(agentID)
	}
	if err := s.store.SetModeCapability(agentID, typeID, body.Kind, body.Ref, body.Enabled); err != nil {
		slog.Error("set agent capability", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.emitCapabilitiesChanged(body.Kind)
	s.recordAudit(r, "capabilities.agent.set", body.Kind, agentID, map[string]any{"ref": body.Ref, "enabled": body.Enabled, "user_id": userID})
	writeJSON(w, http.StatusOK, map[string]string{"agent_id": agentID, "status": "updated"})
}

func (s *Server) restDeleteAgentCapability(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentID")
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	owner, err := s.store.AgentOwner(agentID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}
	if owner != userID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not the agent owner"})
		return
	}
	kind, ref := r.URL.Query().Get("kind"), r.URL.Query().Get("ref")
	if kind == "" || ref == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind and ref are required"})
		return
	}
	typeID := r.URL.Query().Get("type_id")
	if typeID == "" {
		typeID = s.store.DefaultModeTypeID(agentID)
	}
	if err := s.store.DeleteModeCapability(agentID, typeID, kind, ref); err != nil {
		slog.Error("delete agent capability", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.emitCapabilitiesChanged(kind)
	s.recordAudit(r, "capabilities.agent.delete", kind, agentID, map[string]any{"ref": ref, "user_id": userID})
	writeJSON(w, http.StatusOK, map[string]string{"agent_id": agentID, "status": "deleted"})
}

// forwardToAdmin relays one console/user request to the module that owns the
// catalogue, preserving the method, body and query, and passing the admin
// module's own status back.
func (s *Server) forwardToAdmin(w http.ResponseWriter, r *http.Request, method, path string) {
	if s.admin == nil || !s.admin.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin module is not configured"})
		return
	}
	status, body, err := s.admin.Do(r.Context(), method, path, r.Body, r.Header.Get("Content-Type"))
	if err != nil {
		slog.Error("forward to admin", "path", path, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "admin unavailable"})
		return
	}
	if ct := "application/json"; ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
