package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// handleListAgentTypes returns agent definitions; session_only restricts the
// result to session-capable definitions.
func (s *Server) handleListAgentTypes(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.ListAgentTypesRequest
	_ = msg.DecodePayload(&req)
	types, err := s.store.ListAgentTypes(req.SessionOnly)
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("list agent types: " + err.Error())
		return &resp
	}
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeAgentTypes, nanoprotocol.AgentTypesResponse{Types: types})
	return &resp
}

// handleSkillSave forwards a user-scoped skill write to the catalogue's owner.
// The runtime keeps the protocol message it knows; the collection has one writer.
func (s *Server) handleSkillSave(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SkillSaveRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	var def nanoprotocol.SkillDef
	if err := s.adminPost("/api/v1/internal/skills/save", req, &def); err != nil {
		resp := nanoprotocol.NewErrorMessage(err.Error())
		return &resp
	}
	s.emitCapabilitiesChanged("skill")
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSkillSaved, def)
	return &resp
}

// handleSkillDelete forwards a user-scoped skill deletion to the catalogue's owner.
func (s *Server) handleSkillDelete(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SkillDeleteRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := s.adminPost("/api/v1/internal/skills/delete", req, &out); err != nil {
		resp := nanoprotocol.NewErrorMessage(err.Error())
		return &resp
	}
	s.emitCapabilitiesChanged("skill")
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSkillDeleted, map[string]string{"id": out.ID})
	return &resp
}

// handleSkillAck forwards a user's acknowledgement to the catalogue's owner.
func (s *Server) handleSkillAck(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SkillAckRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	var out map[string]any
	if err := s.adminPost("/api/v1/internal/skills/ack", req, &out); err != nil {
		resp := nanoprotocol.NewErrorMessage(err.Error())
		return &resp
	}
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSkillAcked, map[string]any{"skill_id": req.SkillID, "version": req.Version})
	return &resp
}

// handleSetAgentPermission replaces an agent's instance permission overrides.
func (s *Server) handleSetAgentPermission(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SetAgentPermissionRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.AgentID == "" {
		resp := nanoprotocol.NewErrorMessage("agent_id is required")
		return &resp
	}
	typeID := req.TypeID
	if typeID == "" {
		typeID = s.store.DefaultModeTypeID(req.AgentID)
	}
	// One rule at a time, appended to the mode's override — the document is the
	// admin module's, so the write goes there.
	if err := s.adminPost("/api/v1/internal/agents/"+url.PathEscape(req.AgentID)+"/permissions", map[string]any{
		"type_id": typeID, "permissions": req.Permissions, "append": true,
	}, nil); err != nil {
		resp := nanoprotocol.NewErrorMessage("set agent permission: " + err.Error())
		return &resp
	}
	s.emitCapabilitiesChanged("permissions")
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeAgentPermissionSet, map[string]string{
		"agent_id": req.AgentID, "status": "ok",
	})
	return &resp
}

// validSkillName enforces a filesystem-safe skill name (also used as the
// materialized directory name).
func validSkillName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 96 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}

// adminPost posts a JSON body to the admin module and decodes the answer.
func (s *Server) adminPost(path string, body, out any) error {
	return s.adminSend(http.MethodPost, path, body, out)
}

// adminPut is adminPost with the method the console's mode form uses.
func (s *Server) adminPut(path string, body, out any) error {
	return s.adminSend(http.MethodPut, path, body, out)
}

// adminSend posts a JSON body to the admin module and decodes the answer.
func (s *Server) adminSend(method, path string, body, out any) error {
	if s.admin == nil || !s.admin.Enabled() {
		return fmt.Errorf("the admin module is not configured")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, data, err := s.admin.Do(ctx, method, path, bytes.NewReader(payload), "application/json")
	if err != nil {
		return err
	}
	if status >= http.StatusBadRequest {
		return fmt.Errorf("catalogue rejected the write (%d): %s", status, bytes.TrimSpace(data))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}
