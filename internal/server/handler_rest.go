package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func (s *Server) registerRESTRoutes(mux *http.ServeMux) {
	// Inbound (gateway → bus)
	mux.HandleFunc("POST /api/v1/inbound", s.restPostInbound)
	mux.HandleFunc("POST /api/v1/inbound/poll", s.restPollInbound)

	// Generic RPC envelope (nanoprotocol.Client transport)
	mux.HandleFunc("POST /api/v1/rpc", s.restRPC)

	// Outbound (agent → bus)
	mux.HandleFunc("POST /api/v1/outbound", s.restPostOutbound)
	mux.HandleFunc("POST /api/v1/outbound/chunk", s.restOutboundChunk)
	mux.HandleFunc("POST /api/v1/outbound/complete", s.restOutboundComplete)

	// SSE streaming (gateway ← bus)
	mux.HandleFunc("GET /api/v1/outbound/stream", s.restOutboundStream)
	mux.HandleFunc("GET /api/v1/outbound/cursor", s.restOutboundCursor)
	mux.HandleFunc("GET /api/v1/events/stream", s.restEventsStream)

	// Hypervisor tunnels: kept for containers whose image still dials the bus.
	// Tunnels terminate at the provisioner now (see internal/server/hypervisor_tunnel.go).
	mux.HandleFunc("GET /ws/hypervisor", s.handleHypervisorTunnelWS)
	// The relay surface the workspace module uses for those older containers.
	mux.HandleFunc("POST /api/v1/internal/tunnels/{name}/request", s.restForwardTunnelRequest)

	mux.HandleFunc("GET /api/v1/agents/{agentID}/terminals", s.restTerminalList)
	mux.HandleFunc("POST /api/v1/agents/{agentID}/terminals/attach", s.restTerminalAttach)
	mux.HandleFunc("DELETE /api/v1/agents/{agentID}/terminals/{terminalID}", s.restTerminalKill)
	mux.HandleFunc("GET /ws/terminal", s.handleTerminalBridgeWS)

	// Users
	mux.HandleFunc("POST /api/v1/users/resolve", s.restResolveUser)
	mux.HandleFunc("GET /api/v1/users", s.restListUsers)
	// Account administration, for the portal: it used to read `users` straight
	// out of MySQL, which is what made one account two services' business.
	mux.HandleFunc("GET /api/v1/user-accounts", s.restListUserAccounts)
	mux.HandleFunc("GET /api/v1/user-accounts/counts", s.restCountUserAccounts)
	mux.HandleFunc("GET /api/v1/user-accounts/{userID}", s.restGetUserAccount)
	mux.HandleFunc("PATCH /api/v1/user-accounts/{userID}", s.restSetUserAccount)
	mux.HandleFunc("GET /api/v1/user-accounts/{userID}/password-hash", s.restGetUserPasswordHash)
	mux.HandleFunc("PUT /api/v1/user-accounts/{userID}/password", s.restSetUserPassword)
	mux.HandleFunc("GET /api/v1/users/{id}", s.restGetUser)

	// The three surfaces that used to be the console's and the auth service's own
	// tables: directory links and git-browser shortcuts (the session tickets are
	// the auth service's own now). See
	// internal/store/identity.go for why they moved in here.
	mux.HandleFunc("GET /api/v1/users/{userID}/oauth-links", s.restListOAuthLinks)
	mux.HandleFunc("POST /api/v1/users/{userID}/oauth-links", s.restLinkOAuth)
	mux.HandleFunc("DELETE /api/v1/users/{userID}/oauth-links/{provider}", s.restUnlinkOAuth)
	mux.HandleFunc("GET /api/v1/oauth-links", s.restListOAuthLinksByProvider)
	mux.HandleFunc("GET /api/v1/users/{userID}/git-shortcuts", s.restListGitShortcuts)
	mux.HandleFunc("POST /api/v1/users/{userID}/git-shortcuts", s.restAddGitShortcut)
	mux.HandleFunc("DELETE /api/v1/users/{userID}/git-shortcuts/{id}", s.restDeleteGitShortcut)

	// Agents and their groups are the portal's data now. The management paths
	// (/api/v1/agents, /api/v1/agent-groups, the attention snapshot) are served
	// there; the bus keeps only the agent-facing surfaces it is still the
	// address of — terminals above and the supervisors' protocol messages.

	// Sessions and the conversation log are the portal's: it owns
	// portal_sessions, so it serves them. Nothing about a conversation travels
	// through here any more.

	// Config

	// Containers + runners
	mux.HandleFunc("POST /api/v1/containers", s.restRequestContainer)
	mux.HandleFunc("GET /api/v1/containers", s.restListContainers)
	// The queue's poll and status paths are the runner's own: it polls its store
	// directly now, so nothing needs them here.
	mux.HandleFunc("POST /api/v1/containers/{id}/dispose", s.restDisposeContainer)
	mux.HandleFunc("POST /api/v1/containers/{id}/restart", s.restRestartContainer)
	mux.HandleFunc("POST /api/v1/containers/{id}/reprovision", s.restReprovisionContainer)
	mux.HandleFunc("POST /api/v1/runners/register", s.restRegisterRunner)
	// The fleet view and the runtime snapshot, for the portal.
	// Internal: the admin module owns the type catalogue and asks this service
	// (the owner of the agent rows) to clear the references before it deletes a
	// type. Guarded like every other internal route.
	mux.HandleFunc("POST /api/v1/internal/agent-types/{id}/clear-refs", s.restClearAgentTypeRefs)
	// The portal owns the agent rows; the provisioning queue is still here.
	mux.HandleFunc("POST /api/v1/internal/container-requests", s.restInternalContainerRequest)
	// The portal asks for these two broadcasts after it writes an agent, so the
	// supervisors (agents_changed) and the running processes (settings_changed)
	// keep reacting exactly as they did when this service wrote the rows.
	mux.HandleFunc("POST /api/v1/internal/agents-changed", s.restInternalAgentsChanged)
	mux.HandleFunc("POST /api/v1/internal/agents-settings-changed", s.restInternalAgentSettingsChanged)
	// The admin module owns the capability catalogue and asks for this after a
	// write, so every running agent re-pulls its catalogue.
	mux.HandleFunc("POST /api/v1/internal/capabilities-changed", s.restInternalCapabilitiesChanged)
	mux.HandleFunc("GET /api/v1/runners", s.restListRunners)
	mux.HandleFunc("GET /api/v1/runners/counts", s.restCountRunners)
	// The console's own view of the same collection: it may omit the user (the
	// runtime snapshot shows every container) and it shows the runner a container
	// landed on, which the agent-facing shape has no reason to carry.
	mux.HandleFunc("GET /api/v1/container-requests", s.restListContainerRequests)
	mux.HandleFunc("GET /api/v1/container-requests/counts", s.restCountContainerRequests)
	mux.HandleFunc("GET /api/v1/agent-fleet", s.restListAgentFleet)
	mux.HandleFunc("GET /api/v1/user-activity", s.restUserActivity)
	// The overview's remaining numbers: queue depths, the WeChat KF account count,
	// and the two configuration figures.
	mux.HandleFunc("GET /api/v1/queues/counts", s.restCountQueues)
	mux.HandleFunc("GET /api/v1/wechat-kf/counts", s.restCountWeChatKF)
	mux.HandleFunc("DELETE /api/v1/runners/{id}", s.restDeleteRunner)

	// Auth reverse proxy → nanoauth
	s.registerAuthProxy(mux)

	// Enterprise admin: the audit trail is the admin module's collection; these
	// two routes forward to it, so a caller that addresses the bus still works.
	mux.HandleFunc("GET /api/v1/audit", s.restListAudit)
	mux.HandleFunc("POST /api/v1/audit", s.restRecordAudit)

	// Capability catalog: admin management
	mux.HandleFunc("GET /api/v1/capabilities/builtin", s.restListBuiltin)
	mux.HandleFunc("PUT /api/v1/capabilities/builtin/{id}", s.restUpdateBuiltin)
	// Agent types (platform policy plane) + permission keys
	// Prompt baseline: where each type's stored prompt stands relative to the
	// bundle this binary ships, and the single-type restore behind that.
	mux.HandleFunc("GET /api/v1/capabilities/mcp", s.restListMcpAdmin)
	mux.HandleFunc("POST /api/v1/capabilities/mcp", s.restCreateMcpAdmin)
	mux.HandleFunc("POST /api/v1/capabilities/mcp/test", s.restTestMcpAdmin)
	mux.HandleFunc("PUT /api/v1/capabilities/mcp/{id}", s.restUpdateMcpAdmin)
	mux.HandleFunc("PUT /api/v1/capabilities/mcp/{id}/state", s.restUpdateMcpState)
	mux.HandleFunc("DELETE /api/v1/capabilities/mcp/{id}", s.restDeleteMcpAdmin)
	mux.HandleFunc("GET /api/v1/capabilities/skills", s.restListSkillsAdmin)
	mux.HandleFunc("POST /api/v1/capabilities/skills", s.restCreateSkillAdmin)
	mux.HandleFunc("PUT /api/v1/capabilities/skills/{id}", s.restUpdateSkillAdmin)
	mux.HandleFunc("PUT /api/v1/capabilities/skills/{id}/state", s.restUpdateSkillState)
	mux.HandleFunc("DELETE /api/v1/capabilities/skills/{id}", s.restDeleteSkillAdmin)

	// Capability catalog: user-scoped library + per-agent settings
	mux.HandleFunc("GET /api/v1/user/capabilities/library/mcp", s.restListMyMcp)
	mux.HandleFunc("POST /api/v1/user/capabilities/library/mcp", s.restCreateMyMcp)
	mux.HandleFunc("POST /api/v1/user/capabilities/library/mcp/test", s.restTestMcpMy)
	mux.HandleFunc("PUT /api/v1/user/capabilities/library/mcp/{id}", s.restUpdateMyMcp)
	mux.HandleFunc("DELETE /api/v1/user/capabilities/library/mcp/{id}", s.restDeleteMyMcp)
	mux.HandleFunc("GET /api/v1/user/capabilities/library/skills", s.restListMySkills)
	mux.HandleFunc("POST /api/v1/user/capabilities/library/skills", s.restCreateMySkill)
	mux.HandleFunc("PUT /api/v1/user/capabilities/library/skills/{id}", s.restUpdateMySkill)
	mux.HandleFunc("DELETE /api/v1/user/capabilities/library/skills/{id}", s.restDeleteMySkill)
	mux.HandleFunc("GET /api/v1/user/capabilities/agents/{agentID}", s.restAgentCapabilities)
	mux.HandleFunc("PUT /api/v1/user/capabilities/agents/{agentID}", s.restSetAgentCapability)
	mux.HandleFunc("DELETE /api/v1/user/capabilities/agents/{agentID}", s.restDeleteAgentCapability)
	mux.HandleFunc("PUT /api/v1/user/capabilities/agents/{agentID}/permissions", s.restSetAgentPermissions)
	// Per-(agent, mode) override config (model/thinking/settings).
	mux.HandleFunc("GET /api/v1/user/agents/{agentID}/modes/{typeID}", s.restGetAgentMode)
	mux.HandleFunc("PUT /api/v1/user/agents/{agentID}/modes/{typeID}", s.restSetAgentMode)

	// Assignable conversational agent types for new agents.
	// Prompt blocks of the session agents (read-only on the user plane).
	// The account's language: the user-level half of the two-level setting.
	mux.HandleFunc("GET /api/v1/user/preferences", s.restGetPreferences)
	mux.HandleFunc("PUT /api/v1/user/preferences", s.restSetPreferences)
	// Skill update acknowledgement + user-supplied secrets.
	mux.HandleFunc("POST /api/v1/user/skill-acks", s.restUserSkillAck)
	mux.HandleFunc("GET /api/v1/user/secrets/{kind}/{ref}", s.restListUserSecrets)
	mux.HandleFunc("PUT /api/v1/user/secrets/{kind}/{ref}", s.restSetUserSecret)
	mux.HandleFunc("DELETE /api/v1/user/secrets/{kind}/{ref}", s.restDeleteUserSecret)

	// Prompt blocks of the session agents (admin).

	// Model catalog: admin (global) management
	// Give one platform model to every seeded agent type (console action).

	// Model catalog: user library (own scope)

	// Voice input: platform-configured dictation for the browser composer. The
	// audio itself travels over the WebSocket below; these are the settings, the
	// one-time session mint and the post-recording polish pass.
	// The user-facing prompt view still belongs here until the agent rows move:
	// it checks the agent's owner, which is this service's data.
	mux.HandleFunc("GET /api/v1/user/agents/{agentID}/prompt-blocks", s.restGetAgentPromptBlocks)
	mux.HandleFunc("GET /api/v1/user/voice/config", s.restVoiceConfig)
	mux.HandleFunc("POST /api/v1/user/voice/session", s.restVoiceSession)
	mux.HandleFunc("POST /api/v1/user/voice/polish", s.restVoicePolish)
	mux.HandleFunc("POST /api/v1/user/voice/refine", s.restVoiceRefine)
	mux.HandleFunc("POST /api/v1/user/voice/transcribe", s.restVoiceTranscribe)
	mux.HandleFunc("GET "+voiceWSPath, s.handleVoiceWS)
}

func (s *Server) restPostInbound(w http.ResponseWriter, r *http.Request) {
	// Bound the request body to the content cap plus framing slack so an
	// oversized message is rejected before it is buffered in memory.
	r.Body = http.MaxBytesReader(w, r.Body, int64(s.maxContentBytes)+maxBodySlack)
	var msg nanoprotocol.InboundMessage
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

	if err := s.store.PostInbound(msg); err != nil {
		slog.Error("post inbound", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

func (s *Server) restResolveUser(w http.ResponseWriter, r *http.Request) {
	var req nanoprotocol.ResolveUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	userID, isNew, err := s.store.ResolveUser(req.Channel, req.SenderID)
	if err != nil {
		slog.Error("resolve user", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"user_id": userID,
		"is_new":  isNew,
	})
}

func (s *Server) restListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers()
	if err != nil {
		slog.Error("list users", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	type userInfo struct {
		ID string `json:"id"`
	}

	result := make([]userInfo, 0, len(users))
	for _, u := range users {
		result = append(result, userInfo{ID: u})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"users": result,
		"count": len(result),
	})
}

func (s *Server) restRequestContainer(w http.ResponseWriter, r *http.Request) {
	var req nanoprotocol.RequestContainerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	if err := s.callProvisioner(r, func(ctx context.Context, c *provisionerclient.Client) error {
		return c.Enqueue(ctx, req.UserID, req.BindingID, req.AgentID, req.Level)
	}); err != nil {
		s.writeProvisionerError(w, err, "request container", req.UserID)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
}

func (s *Server) restListContainers(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	requests, err := s.store.ListContainers(userID, 0)
	if err != nil {
		slog.Error("list containers", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	requests = nonNilSlice(requests)
	writeJSON(w, http.StatusOK, map[string]any{"requests": requests})
}

func (s *Server) restDisposeContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "container id is required"})
		return
	}

	if err := s.callProvisioner(r, func(ctx context.Context, c *provisionerclient.Client) error {
		return c.Dispose(ctx, id)
	}); err != nil {
		s.writeProvisionerError(w, err, "dispose container", id)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "disposing"})
}

// nonNilSlice keeps a list field on the wire an array. A nil slice means "no
// elements" in Go and serialises to null, which every console client then has
// to guard against — the capability page read `.length` on it and crashed on a
// fresh installation whose catalog was still empty.
func nonNilSlice[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
