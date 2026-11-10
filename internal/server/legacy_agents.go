package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// Legacy agent/session messages, kept as a bridge for containers that still run
// the previous runtime image.
//
// The protocol no longer carries these (v0.44.0): the portal owns the agent rows
// and the conversation projection, and the current runtime asks it directly. But
// agent containers are long-lived and are replaced on the operator's schedule, so
// for one deployment round the bus answers the three messages an older runtime
// still sends by forwarding them — a rollout must not silence a user's history or
// strand a newly created agent because its supervisor cannot list agents.
//
// Remove this file (and its dispatch cases) once every agent container runs an
// image built after v0.44.0.
const (
	legacyTypeListAgents        = "list_agents"
	legacyTypeSaveSession       = "save_session"
	legacyTypeUpdateAgentStatus = "update_agent_status"
)

// legacyType reports whether msg is one of the bridged messages.
func legacyType(typ string) bool {
	switch typ {
	case legacyTypeListAgents, legacyTypeSaveSession, legacyTypeUpdateAgentStatus:
		return true
	}
	return false
}

func (s *Server) handleLegacyAgentMessage(msg nanoprotocol.Message) *nanoprotocol.Message {
	switch msg.Type {
	case legacyTypeListAgents:
		return s.legacyListAgents(msg)
	case legacyTypeSaveSession:
		return s.legacySaveSession(msg)
	default:
		return s.legacyUpdateAgentStatus(msg)
	}
}

// legacyListAgents answers with the shape the old client decodes.
func (s *Server) legacyListAgents(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req struct {
		UserID string `json:"user_id"`
	}
	if err := msg.DecodePayload(&req); err != nil || req.UserID == "" {
		resp := nanoprotocol.NewErrorMessage("user_id is required")
		return &resp
	}
	var out struct {
		Agents json.RawMessage `json:"agents"`
	}
	if err := s.portalGet("/internal/v1/agents?user_id="+url.QueryEscape(req.UserID), &out); err != nil {
		slog.Error("legacy list agents", "user_id", req.UserID, "error", err)
		resp := nanoprotocol.NewErrorMessage("list agents failed")
		return &resp
	}
	if len(out.Agents) == 0 {
		out.Agents = json.RawMessage("[]")
	}
	payload := map[string]json.RawMessage{"agents": out.Agents}
	resp, _ := nanoprotocol.NewMessage("agents_listed", payload)
	return &resp
}

// legacySaveSession forwards a snapshot to the portal. A 409 means "send the
// whole conversation", which the old client understands as Resync.
func (s *Server) legacySaveSession(msg nanoprotocol.Message) *nanoprotocol.Message {
	var body json.RawMessage
	if err := msg.DecodePayload(&body); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	status, err := s.portalPost("/internal/v1/sessions", body, nil)
	// 409 is an answer, not a failure: the portal does not hold the prefix this
	// delta follows.
	if status == http.StatusConflict {
		var head struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(body, &head)
		resp, _ := nanoprotocol.NewMessage("session_saved", map[string]any{"session_id": head.SessionID, "resync": true})
		return &resp
	}
	if err != nil {
		slog.Error("legacy save session", "error", err)
		resp := nanoprotocol.NewErrorMessage("save session failed")
		return &resp
	}
	var sessionID string
	var head struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(body, &head)
	sessionID = head.SessionID
	resp, _ := nanoprotocol.NewMessage("session_saved", map[string]any{"session_id": sessionID})
	return &resp
}

func (s *Server) legacyUpdateAgentStatus(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req struct {
		AgentID string `json:"agent_id"`
		Status  string `json:"status"`
	}
	if err := msg.DecodePayload(&req); err != nil || req.AgentID == "" || req.Status == "" {
		resp := nanoprotocol.NewErrorMessage("agent_id and status are required")
		return &resp
	}
	payload, _ := json.Marshal(map[string]string{"status": req.Status})
	if _, err := s.portalPost("/internal/v1/agents/"+url.QueryEscape(req.AgentID)+"/status", payload, nil); err != nil {
		slog.Error("legacy agent status", "agent_id", req.AgentID, "error", err)
		resp := nanoprotocol.NewErrorMessage("update agent status failed")
		return &resp
	}
	resp := nanoprotocol.Message{Type: "ok"}
	return &resp
}

// portalGet/portalPost reach the portal with the deployment's shared token.
func (s *Server) portalGet(path string, out any) error {
	if s.portal == nil || !s.portal.Enabled() {
		return fmt.Errorf("portal module is not configured")
	}
	ctx, cancel := portalContext()
	defer cancel()
	return s.portal.GetJSON(ctx, path, out)
}

func (s *Server) portalPost(path string, body []byte, out any) (int, error) {
	if s.portal == nil || !s.portal.Enabled() {
		return 0, fmt.Errorf("portal module is not configured")
	}
	ctx, cancel := portalContext()
	defer cancel()
	return s.portal.PostJSON(ctx, path, body, out)
}
