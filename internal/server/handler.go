package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanoprotocol"
	"github.com/gorilla/websocket"
)

func (s *Server) handleConnection(conn *websocket.Conn) {
	for {
		var msg nanoprotocol.Message
		if err := conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				slog.Warn("websocket read error", "error", err)
			}
			return
		}

		resp := s.handleMessage(msg)
		if resp != nil {
			if err := conn.WriteJSON(resp); err != nil {
				slog.Warn("websocket write error", "error", err)
				return
			}
		}
	}
}

func (s *Server) handleMessage(msg nanoprotocol.Message) *nanoprotocol.Message {
	switch msg.Type {
	case nanoprotocol.TypeResolveUser:
		return s.handleResolveUser(msg)
	case nanoprotocol.TypePostInbound:
		return s.handlePostInbound(msg)
	case nanoprotocol.TypePollInbound:
		return s.handlePollInbound(msg)
	case nanoprotocol.TypePostOutbound:
		return s.handlePostOutbound(msg)
	case nanoprotocol.TypePollOutbound:
		return s.handlePollOutbound(msg)
	// The queue's poll/request/status/dispose messages are gone: the runner
	// polls its own store now. list_containers stays for the console's read.
	case nanoprotocol.TypeListContainers:
		return s.handleListContainers(msg)
	case nanoprotocol.TypeRegisterRunner:
		return s.handleRegisterRunner(msg)
	case nanoprotocol.TypeValidateToken:
		return s.handleValidateToken(msg)
	// create/update/delete over the protocol are gone with the agent rows: the
	// portal serves the management surface, and the supervisors and the gateway
	// read it over HTTP. update_agent_status remains as the one write, and is
	// forwarded to the portal, which owns the row.
	case nanoprotocol.TypeUpdateUserStatus:
		return s.handleUpdateUserStatus(msg)
	case nanoprotocol.TypeGetUserInfo:
		return s.handleGetUserInfo(msg)
	case nanoprotocol.TypeGetWeChatKFSyncState:
		return s.handleGetWeChatKFSyncState(msg)
	case nanoprotocol.TypeUpdateWeChatKFCursor:
		return s.handleUpdateWeChatKFCursor(msg)
	case nanoprotocol.TypeUpdateWeChatKFCallbackTok:
		return s.handleUpdateWeChatKFCallbackToken(msg)
	case nanoprotocol.TypeGetCapabilities:
		return s.handleGetCapabilities(msg)
	case nanoprotocol.TypeGetAgentSnapshot:
		return s.handleGetAgentSnapshot(msg)
	case nanoprotocol.TypeBuiltinToolsSync:
		return s.handleBuiltinToolsSync(msg)
	case nanoprotocol.TypeSkillSave:
		return s.handleSkillSave(msg)
	case nanoprotocol.TypeSkillDelete:
		return s.handleSkillDelete(msg)
	case nanoprotocol.TypeSkillAck:
		return s.handleSkillAck(msg)
	case nanoprotocol.TypeListAgentTypes:
		return s.handleListAgentTypes(msg)
	case nanoprotocol.TypeSetAgentPermission:
		return s.handleSetAgentPermission(msg)
	default:
		resp := nanoprotocol.NewErrorMessage("unknown message type: " + msg.Type)
		return &resp
	}
}

func (s *Server) handleResolveUser(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.ResolveUserRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	resolved, err := s.store.ResolveUserBinding(req.Channel, req.SenderID)
	if err != nil {
		slog.Debug("resolve user failed", "channel", req.Channel, "sender", req.SenderID, "error", err)
		resp := nanoprotocol.NewErrorMessage(err.Error())
		return &resp
	}

	status := "active"
	if resolved.IsNew {
		status = "pending"
	} else {
		st, _, err := s.store.GetUserInfo(resolved.UserID)
		if err == nil {
			status = st
		}
	}

	slog.Debug("resolved user", "channel", req.Channel, "sender", req.SenderID, "user_id", resolved.UserID, "binding_id", resolved.BindingID, "is_new", resolved.IsNew)
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeUserResolved, nanoprotocol.ResolveUserResponse{
		UserID:    resolved.UserID,
		BindingID: resolved.BindingID,
		IsNew:     resolved.IsNew,
		Status:    status,
	})
	return &resp
}

func (s *Server) handlePostInbound(msg nanoprotocol.Message) *nanoprotocol.Message {
	var inbound nanoprotocol.InboundMessage
	if err := msg.DecodePayload(&inbound); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if err := s.checkContent(inbound.Content); err != nil {
		resp := nanoprotocol.NewErrorMessage(err.Error())
		return &resp
	}
	if err := s.store.PostInbound(inbound); err != nil {
		slog.Debug("post inbound failed", "user_id", inbound.UserID, "error", err)
		resp := nanoprotocol.NewErrorMessage("post inbound: " + err.Error())
		return &resp
	}

	slog.Debug("posted inbound message", "user_id", inbound.UserID, "channel", inbound.Channel, "content_len", len(inbound.Content))
	resp := nanoprotocol.Message{Type: "ok"}
	return &resp
}

func (s *Server) handlePollInbound(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.PollInboundRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	inbound, err := s.store.ClaimInbound(req.UserID, req.BindingID, req.AgentID)
	if err != nil {
		slog.Error("claim inbound failed", "error", err)
		resp := nanoprotocol.NewErrorMessage("claim inbound: " + err.Error())
		return &resp
	}
	if inbound == nil {
		resp := nanoprotocol.Message{Type: "ok"}
		return &resp
	}

	slog.Debug("claimed inbound message", "user_id", req.UserID, "agent_id", req.AgentID, "content_len", len(inbound.Content))
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeInbound, inbound)
	return &resp
}

func (s *Server) handlePostOutbound(msg nanoprotocol.Message) *nanoprotocol.Message {
	var outbound nanoprotocol.OutboundMessage
	if err := msg.DecodePayload(&outbound); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if err := s.checkContent(outbound.Content); err != nil {
		resp := nanoprotocol.NewErrorMessage(err.Error())
		return &resp
	}
	if err := s.store.PostOutbound(outbound); err != nil {
		slog.Debug("post outbound failed", "user_id", outbound.UserID, "error", err)
		resp := nanoprotocol.NewErrorMessage("post outbound: " + err.Error())
		return &resp
	}

	slog.Debug("posted outbound message", "user_id", outbound.UserID, "channel", outbound.Channel, "content_len", len(outbound.Content))

	s.broadcastOutbound(outbound)

	resp := nanoprotocol.Message{Type: "ok"}
	return &resp
}

func (s *Server) handlePollOutbound(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.PollOutboundRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	outbound, err := s.store.ClaimOutbound(req.Channel, req.GatewayID)
	if err != nil {
		slog.Error("claim outbound failed", "error", err)
		resp := nanoprotocol.NewErrorMessage("claim outbound: " + err.Error())
		return &resp
	}
	if outbound == nil {
		resp := nanoprotocol.Message{Type: "ok"}
		return &resp
	}

	slog.Debug("claimed outbound message", "channel", req.Channel, "gateway_id", req.GatewayID, "content_len", len(outbound.Content))
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeOutbound, outbound)
	return &resp
}

func (s *Server) handleListContainers(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.ListContainersRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	requests, err := s.store.ListContainers(req.UserID, req.Limit)
	if err != nil {
		slog.Error("list containers failed", "user_id", req.UserID, "error", err)
		resp := nanoprotocol.NewErrorMessage("list containers: " + err.Error())
		return &resp
	}

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeContainersListed, nanoprotocol.ListContainersResponse{
		Requests: requests,
	})
	return &resp
}

func (s *Server) handleUpdateUserStatus(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.UpdateUserStatusRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if err := s.store.UpdateUserStatus(req.UserID, req.Status); err != nil {
		slog.Error("update user status failed", "user_id", req.UserID, "error", err)
		resp := nanoprotocol.NewErrorMessage("update user status: " + err.Error())
		return &resp
	}

	slog.Debug("updated user status", "user_id", req.UserID, "status", req.Status)
	resp := nanoprotocol.Message{Type: "ok"}
	return &resp
}

func (s *Server) handleGetUserInfo(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.GetUserInfoRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	status, level, err := s.store.GetUserInfo(req.UserID)
	if err != nil {
		slog.Error("get user info failed", "user_id", req.UserID, "error", err)
		resp := nanoprotocol.NewErrorMessage("get user info: " + err.Error())
		return &resp
	}

	slog.Debug("got user info", "user_id", req.UserID, "status", status, "level", level)
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeUserInfo, nanoprotocol.UserInfoResponse{
		UserID: req.UserID,
		Status: status,
		Level:  level,
	})
	return &resp
}

func (s *Server) handleGetWeChatKFSyncState(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.GetWeChatKFSyncStateRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	state, err := s.store.GetWeChatKFSyncState(req.OpenKfID)
	if err != nil {
		slog.Error("get wechatkf sync state failed", "open_kfid", req.OpenKfID, "error", err)
		resp := nanoprotocol.NewErrorMessage("get wechatkf sync state: " + err.Error())
		return &resp
	}
	if state == nil {
		state = &nanoprotocol.WeChatKFSyncStateResponse{Found: false}
	}

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeWeChatKFSyncState, state)
	return &resp
}

func (s *Server) handleUpdateWeChatKFCursor(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.UpdateWeChatKFCursorRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if err := s.store.UpdateWeChatKFCursor(req.OpenKfID, req.Cursor); err != nil {
		slog.Error("update wechatkf cursor failed", "open_kfid", req.OpenKfID, "error", err)
		resp := nanoprotocol.NewErrorMessage("update wechatkf cursor: " + err.Error())
		return &resp
	}

	resp := nanoprotocol.Message{Type: "ok"}
	return &resp
}

func (s *Server) handleUpdateWeChatKFCallbackToken(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.UpdateWeChatKFCallbackTokenRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if err := s.store.UpdateWeChatKFCallbackToken(req.OpenKfID, req.CallbackToken, req.TokenExpiresAt); err != nil {
		slog.Error("update wechatkf callback token failed", "open_kfid", req.OpenKfID, "error", err)
		resp := nanoprotocol.NewErrorMessage("update wechatkf callback token: " + err.Error())
		return &resp
	}

	resp := nanoprotocol.Message{Type: "ok"}
	return &resp
}

func (s *Server) handleGetCapabilities(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.GetCapabilitiesRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.AgentID == "" {
		resp := nanoprotocol.NewErrorMessage("agent_id is required")
		return &resp
	}

	capabilities, err := s.store.ResolveCapabilities(req.AgentID)
	if err != nil {
		slog.Error("get capabilities failed", "agent_id", req.AgentID, "error", err)
		resp := nanoprotocol.NewErrorMessage("get capabilities: " + err.Error())
		return &resp
	}

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeCapabilitiesResp, capabilities)
	return &resp
}

// handleGetAgentSnapshot returns the full reconciler input for an agent in one
// round trip: agent record + agent config items + resolved capabilities (which
// include the visible model catalog).
func (s *Server) handleGetAgentSnapshot(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.GetAgentSnapshotRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.AgentID == "" {
		resp := nanoprotocol.NewErrorMessage("agent_id is required")
		return &resp
	}
	agent, err := s.store.GetAgent(req.AgentID)
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("get agent: " + err.Error())
		return &resp
	}
	if agent == nil {
		resp := nanoprotocol.NewErrorMessage("agent not found: " + req.AgentID)
		return &resp
	}
	caps, err := s.store.ResolveCapabilities(req.AgentID)
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("get capabilities: " + err.Error())
		return &resp
	}
	config, err := s.configItems(context.Background(), "agent")
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("get config: " + err.Error())
		return &resp
	}
	// Overlay the AgentType/agent runtime settings projected as flat config
	// keys, so the agent keeps consuming `agent.*` / `tools.*` unchanged.
	settings, err := s.store.EffectiveAgentSettings(req.AgentID)
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("get settings: " + err.Error())
		return &resp
	}
	var maxRounds int
	if at, err := s.store.AgentTypeForAgent(req.AgentID); err == nil && at != nil {
		maxRounds = at.MaxRounds
	}
	projected := store.SettingsToConfigItems(settings, maxRounds)
	projectedKeys := make(map[string]struct{}, len(projected))
	for _, item := range projected {
		projectedKeys[item.Key] = struct{}{}
	}
	// Drop any stale KV row whose key is now projected, so the effective value
	// is unambiguous even if a deployment still carries the old flat keys.
	merged := config[:0]
	for _, item := range config {
		if _, overridden := projectedKeys[item.Key]; !overridden {
			merged = append(merged, item)
		}
	}
	config = append(merged, projected...)
	defs, err := s.store.ResolveAgentDefinitions(req.AgentID)
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("resolve agent definitions: " + err.Error())
		return &resp
	}
	info := s.agentInfo(*agent)
	info.Settings = s.withUserLanguage(info.Settings, agent.UserID)
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeAgentSnapshot, nanoprotocol.AgentSnapshotResponse{
		Agent:        info,
		Config:       config,
		Capabilities: *caps,
		Agents:       defs,
	})
	return &resp
}

func (s *Server) handleBuiltinToolsSync(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.BuiltinToolsSyncRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	// The catalogue is the admin module's; the runtime keeps announcing through
	// the protocol it knows and this service forwards the announcement.
	payload, err := json.Marshal(map[string]any{"tools": req.Tools})
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("sync builtin tools: " + err.Error())
		return &resp
	}
	if s.admin == nil || !s.admin.Enabled() {
		resp := nanoprotocol.NewErrorMessage("sync builtin tools: the admin module is not configured")
		return &resp
	}
	syncCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := s.admin.PutJSONPath(syncCtx, "/api/v1/internal/builtin-tools/sync", payload); err != nil {
		slog.Error("builtin tools sync failed", "count", len(req.Tools), "error", err)
		resp := nanoprotocol.NewErrorMessage("sync builtin tools: " + err.Error())
		return &resp
	}
	slog.Info("builtin tools synced", "agent_id", req.AgentID, "count", len(req.Tools))
	// The acknowledgement is a plain message: the config item types are gone
	// with the config surface. The agent only checks for an error answer.
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeBuiltinToolsSync, map[string]any{
		"agent_id": req.AgentID,
		"count":    len(req.Tools),
	})
	return &resp
}

func (s *Server) handleRegisterRunner(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.RegisterRunnerRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	// The registry belongs to the provisioner: this service keeps the door older
	// runners knock on and forwards the write.
	if err := s.forwardRunnerRegistration(req); err != nil {
		slog.Error("register runner failed", "error", err)
		resp := nanoprotocol.NewErrorMessage("register runner: " + err.Error())
		return &resp
	}

	slog.Info("runner registered", "runner_id", req.RunnerID, "hostname", req.Hostname)
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeRunnerRegistered, nanoprotocol.RegisterRunnerResponse{
		RunnerID: req.RunnerID,
	})
	return &resp
}
