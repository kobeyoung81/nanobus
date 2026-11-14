package server

// The per-agent capability surface.
//
// The catalogue itself (built-in tools, MCP servers, skills) is the admin module's,
// and the console asks it directly — no forwarding route survives here. What stays
// is the per-agent half: which of those capabilities this agent has, which mode's
// overrides apply, and the permission list for one agent. Those reads resolve the
// catalogue for a running agent, so they stay until the bus stops reading the
// catalogue at all.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// maskValue is the placeholder the console shows instead of a stored secret.
const maskValue = "***"

func userIDFromQuery(r *http.Request) string {
	return r.URL.Query().Get("user_id")
}

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
	// The rules live in the override document, which the admin module owns; this
	// service decided whose agent and which mode it is, and forwards the list.
	if err := s.adminPost("/api/v1/internal/agents/"+url.PathEscape(agentID)+"/permissions", map[string]any{
		"type_id": typeID, "permissions": body.Permissions,
	}, nil); err != nil {
		slog.Error("set agent permissions", "error", err)
		writeAdminFailure(w, err)
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
	// The override document is the admin module's; this service decided what the
	// caller meant (owner, mode, visibility) and forwards the write.
	if err := s.adminPost("/api/v1/internal/agents/"+url.PathEscape(agentID)+"/capabilities", map[string]any{
		"type_id": typeID, "kind": body.Kind, "ref": body.Ref, "enabled": body.Enabled,
	}, nil); err != nil {
		slog.Error("set agent capability", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "catalogue unavailable"})
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
	if err := s.adminPost("/api/v1/internal/agents/"+url.PathEscape(agentID)+"/capabilities/delete", map[string]any{
		"type_id": typeID, "kind": kind, "ref": ref,
	}, nil); err != nil {
		slog.Error("delete agent capability", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "catalogue unavailable"})
		return
	}
	s.emitCapabilitiesChanged(kind)
	s.recordAudit(r, "capabilities.agent.delete", kind, agentID, map[string]any{"ref": ref, "user_id": userID})
	writeJSON(w, http.StatusOK, map[string]string{"agent_id": agentID, "status": "deleted"})
}

// forwardToAdmin relays one console/user request to the module that owns the
// catalogue, preserving the method, body and query, and passing the admin
// module's own status back.
// testProbeTimeout bounds one MCP reachability probe: a server that does not
// answer within it is reported as unreachable rather than left spinning in the
// console.
const testProbeTimeout = 20 * time.Second

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
