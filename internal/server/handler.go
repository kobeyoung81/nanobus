package server

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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
	case nanoprotocol.TypePollPendingContainers:
		return s.handlePollPendingContainers(msg)
	case nanoprotocol.TypeRequestContainer:
		return s.handleRequestContainer(msg)
	case nanoprotocol.TypeListContainers:
		return s.handleListContainers(msg)
	case nanoprotocol.TypeUpdateContainerStatus:
		return s.handleUpdateContainerStatus(msg)
	case nanoprotocol.TypeDisposeContainer:
		return s.handleDisposeContainer(msg)
	case nanoprotocol.TypeRegisterRunner:
		return s.handleRegisterRunner(msg)
	case nanoprotocol.TypeValidateToken:
		return s.handleValidateToken(msg)
	case nanoprotocol.TypeListAgents:
		return s.handleListAgents(msg)
	case nanoprotocol.TypeCreateAgent:
		return s.handleCreateAgent(msg)
	case nanoprotocol.TypeUpdateAgent:
		return s.handleUpdateAgent(msg)
	case nanoprotocol.TypeDeleteAgent:
		return s.handleDeleteAgent(msg)
	case nanoprotocol.TypeUpdateAgentStatus:
		return s.handleUpdateAgentStatus(msg)
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
	case nanoprotocol.TypeGetConfigItems:
		return s.handleGetConfigItems(msg)
	case nanoprotocol.TypeSetConfigItem:
		return s.handleSetConfigItem(msg)
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
	case nanoprotocol.TypeSaveSession:
		return s.handleSaveSession(msg)
	case nanoprotocol.TypeArchiveSession:
		return s.handleArchiveSession(msg)
	case nanoprotocol.TypeListSessions:
		return s.handleListSessions(msg)
	case nanoprotocol.TypeGetSession:
		return s.handleGetSession(msg)
	case nanoprotocol.TypeGetBindingAgent:
		return s.handleGetBindingAgent(msg)
	case nanoprotocol.TypeSetBindingAgent:
		return s.handleSetBindingAgent(msg)
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

func (s *Server) handlePollPendingContainers(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.PollPendingContainersRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	// Touch runner heartbeat on every poll.
	if req.RunnerID != "" {
		_ = s.store.UpdateRunnerHeartbeat(req.RunnerID)
	}

	requests, err := s.store.PollPendingContainers(req.RunnerID)
	if err != nil {
		slog.Error("poll pending containers failed", "error", err)
		resp := nanoprotocol.NewErrorMessage("poll pending containers: " + err.Error())
		return &resp
	}

	slog.Debug("polled pending containers", "runner_id", req.RunnerID, "count", len(requests))
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypePendingContainers, nanoprotocol.PollPendingContainersResponse{
		Requests: requests,
	})
	return &resp
}

func (s *Server) handleRequestContainer(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.RequestContainerRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if req.UserID == "" {
		resp := nanoprotocol.NewErrorMessage("user_id is required")
		return &resp
	}

	if err := s.store.CreateContainerRequest(req.UserID, req.BindingID, req.AgentID, req.Level); err != nil {
		slog.Error("request container failed", "user_id", req.UserID, "agent_id", req.AgentID, "error", err)
		resp := nanoprotocol.NewErrorMessage("request container: " + err.Error())
		return &resp
	}

	slog.Info("container requested", "user_id", req.UserID, "agent_id", req.AgentID, "level", req.Level)
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeContainerRequested, req)
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

func (s *Server) handleUpdateContainerStatus(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.UpdateContainerStatusRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if err := s.store.UpdateContainerStatus(req.UserID, req.BindingID, req.Status); err != nil {
		slog.Error("update container status failed", "user_id", req.UserID, "binding_id", req.BindingID, "error", err)
		resp := nanoprotocol.NewErrorMessage("update container status: " + err.Error())
		return &resp
	}

	slog.Debug("updated container status", "user_id", req.UserID, "binding_id", req.BindingID, "status", req.Status)
	resp := nanoprotocol.Message{Type: "ok"}
	return &resp
}

func (s *Server) handleDisposeContainer(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.DisposeContainerRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if req.UserID == "" {
		resp := nanoprotocol.NewErrorMessage("user_id is required")
		return &resp
	}

	if err := s.store.DisposeContainerRequest(req.UserID, req.BindingID); err != nil {
		slog.Error("dispose container failed", "user_id", req.UserID, "binding_id", req.BindingID, "error", err)
		resp := nanoprotocol.NewErrorMessage("dispose container: " + err.Error())
		return &resp
	}

	slog.Info("container disposal requested", "user_id", req.UserID, "binding_id", req.BindingID, "agent_id", req.AgentID)
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeContainerDisposed, req)
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

func (s *Server) handleGetConfigItems(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.GetConfigItemsRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	items, err := s.store.GetConfigItems(req.ComponentType)
	if err != nil {
		slog.Error("get config items failed", "component_type", req.ComponentType, "error", err)
		resp := nanoprotocol.NewErrorMessage("get config items: " + err.Error())
		return &resp
	}

	slog.Debug("served config items", "component_type", req.ComponentType, "count", len(items))
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeConfigItems, nanoprotocol.GetConfigItemsResponse{
		ComponentType: req.ComponentType,
		Items:         items,
	})
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
	config, err := s.store.GetConfigItems("agent")
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
	if err := s.store.SyncBuiltinTools(req.Tools); err != nil {
		slog.Error("builtin tools sync failed", "count", len(req.Tools), "error", err)
		resp := nanoprotocol.NewErrorMessage("sync builtin tools: " + err.Error())
		return &resp
	}
	slog.Info("builtin tools synced", "agent_id", req.AgentID, "count", len(req.Tools))
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeConfigItemSet, nanoprotocol.SetConfigItemResponse{
		ComponentType: "agent",
		Key:           "capabilities.builtin",
		Value:         "synced",
	})
	return &resp
}

func (s *Server) handleSetConfigItem(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SetConfigItemRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	// The admin module owns the collection; this path forwards like the REST one.
	if s.adminURL == "" {
		resp := nanoprotocol.NewErrorMessage("set config item: admin module is not configured")
		return &resp
	}
	target := s.adminURL + "/api/v1/config/" + url.PathEscape(req.ComponentType) + "/" + url.PathEscape(req.Key)
	httpReq, err := http.NewRequest(http.MethodPut, target, bytes.NewReader(configWriteBody(req.Value, "", "")))
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("set config item: " + err.Error())
		return &resp
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if s.internalToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+s.internalToken)
	}
	if httpResp, err := http.DefaultClient.Do(httpReq); err != nil {
		slog.Error("set config item failed", "component_type", req.ComponentType, "key", req.Key, "error", err)
		resp := nanoprotocol.NewErrorMessage("set config item: " + err.Error())
		return &resp
	} else {
		_ = httpResp.Body.Close()
		if httpResp.StatusCode != http.StatusOK {
			slog.Error("set config item rejected upstream", "component_type", req.ComponentType, "key", req.Key, "status", httpResp.StatusCode)
			resp := nanoprotocol.NewErrorMessage("set config item: admin module rejected the write")
			return &resp
		}
	}

	s.emitConfigChanged(req.Key)

	slog.Info("config item updated", "component_type", req.ComponentType, "key", req.Key)
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeConfigItemSet, nanoprotocol.SetConfigItemResponse{
		ComponentType: req.ComponentType,
		Key:           req.Key,
		Value:         req.Value,
	})
	return &resp
}

func (s *Server) handleRegisterRunner(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.RegisterRunnerRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if err := s.store.RegisterRunner(req.RunnerID, req.Hostname, req.DockerHost); err != nil {
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

func (s *Server) handleSaveSession(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SaveSessionRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	start := time.Now()
	rec := store.SessionRecord{
		ID:          req.SessionID,
		AgentID:     req.AgentID,
		AccountID:   req.AccountID,
		Channel:     req.Channel,
		ChatID:      req.ChatID,
		Messages:    req.Messages,
		Summary:     req.Summary,
		Title:       req.Title,
		Meta:        req.Meta,
		Status:      req.Status,
		TurnRunning: req.TurnRunning,
		// A delta save: the store accumulates the totals from it.
		FromIndex:       req.FromIndex,
		MessagesOmitted: req.MessagesOmitted,
	}
	if err := s.store.SaveSession(rec); err != nil {
		// A delta that does not follow the stored conversation is not a failure to
		// report as one: it is an answer — "send the whole array" — and the sender
		// acts on it immediately. Saying so in the response keeps a resync out of
		// the failure ledger, which is meant for history that is not landing.
		if errors.Is(err, store.ErrSessionResync) {
			slog.Warn("session delta out of sync, asking for a full snapshot",
				"session_id", req.SessionID, "from_index", req.FromIndex)
			resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSessionSaved, nanoprotocol.SaveSessionResponse{
				SessionID: req.SessionID,
				Resync:    true,
			})
			return &resp
		}
		slog.Error("save session failed", "session_id", req.SessionID, "error", err)
		resp := nanoprotocol.NewErrorMessage("save session: " + err.Error())
		return &resp
	}
	slog.Debug("session saved to db",
		"session_id", req.SessionID,
		"messages_bytes", len(req.Messages),
		"delta", req.MessagesOmitted,
		"db_ms", time.Since(start).Milliseconds(),
	)

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSessionSaved, nanoprotocol.SaveSessionResponse{
		SessionID: req.SessionID,
	})
	return &resp
}

func (s *Server) handleArchiveSession(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.GetSessionRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if err := s.store.ArchiveSession(req.SessionID); err != nil {
		slog.Error("archive session failed", "session_id", req.SessionID, "error", err)
		resp := nanoprotocol.NewErrorMessage("archive session: " + err.Error())
		return &resp
	}

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSessionArchived, nanoprotocol.SaveSessionResponse{
		SessionID: req.SessionID,
	})
	return &resp
}

func (s *Server) handleListSessions(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.ListSessionsRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	records, err := s.store.ListSessions(req.AgentID, req.Limit, req.Offset)
	if err != nil {
		slog.Error("list sessions failed", "agent_id", req.AgentID, "error", err)
		resp := nanoprotocol.NewErrorMessage("list sessions: " + err.Error())
		return &resp
	}

	metas := make([]nanoprotocol.SessionMeta, 0, len(records))
	for _, rec := range records {
		metas = append(metas, store.SessionMeta(rec))
	}

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSessionsListed, nanoprotocol.ListSessionsResponse{
		Sessions: metas,
	})
	return &resp
}

func (s *Server) handleGetBindingAgent(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.GetBindingAgentRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	agentID, err := s.store.GetBindingAgent(req.BindingID)
	if err != nil {
		slog.Error("get binding agent failed", "binding_id", req.BindingID, "error", err)
		resp := nanoprotocol.NewErrorMessage("get binding agent: " + err.Error())
		return &resp
	}

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeBindingAgent, nanoprotocol.GetBindingAgentResponse{
		AgentID: agentID,
	})
	return &resp
}

func (s *Server) handleSetBindingAgent(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SetBindingAgentRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	if err := s.store.SetBindingAgent(req.BindingID, req.AgentID); err != nil {
		slog.Error("set binding agent failed", "binding_id", req.BindingID, "error", err)
		resp := nanoprotocol.NewErrorMessage("set binding agent: " + err.Error())
		return &resp
	}

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeBindingAgentSet, nanoprotocol.SetBindingAgentResponse{
		BindingID: req.BindingID,
		AgentID:   req.AgentID,
	})
	return &resp
}

func (s *Server) handleGetSession(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.GetSessionRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}

	rec, err := s.store.GetSession(req.SessionID)
	if err != nil {
		slog.Error("get session failed", "session_id", req.SessionID, "error", err)
		resp := nanoprotocol.NewErrorMessage("get session: " + err.Error())
		return &resp
	}
	if rec == nil {
		resp := nanoprotocol.NewErrorMessage("session not found")
		return &resp
	}

	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSession, nanoprotocol.GetSessionResponse{
		SessionID: rec.ID,
		AgentID:   rec.AgentID,
		Messages:  rec.Messages,
		Summary:   rec.Summary,
	})
	return &resp
}
