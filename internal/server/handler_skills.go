package server

import (
	"log/slog"
	"strings"

	"gitee.com/kobeyoung81/nanobus/internal/skillbundle"
	"gitee.com/kobeyoung81/nanobus/internal/store"
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

// handleSkillSave creates or updates a user-scoped skill on behalf of an agent
// or the portal. Platform skills cannot be written here.
func (s *Server) handleSkillSave(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SkillSaveRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.Name == "" || len(req.Files) == 0 {
		resp := nanoprotocol.NewErrorMessage("name and files are required")
		return &resp
	}
	if req.UserID == "" {
		resp := nanoprotocol.NewErrorMessage("user_id is required")
		return &resp
	}
	if !validSkillName(req.Name) {
		resp := nanoprotocol.NewErrorMessage("invalid skill name")
		return &resp
	}
	origin := req.Origin
	if origin != "agent" {
		origin = "human"
	}
	taken, err := s.store.SkillNameTaken(req.Name, req.UserID, req.ID)
	if err != nil {
		resp := nanoprotocol.NewErrorMessage("check skill name: " + err.Error())
		return &resp
	}
	if taken {
		resp := nanoprotocol.NewErrorMessage("a skill with this name already exists")
		return &resp
	}

	rec := store.SkillRecord{
		Capability: store.Capability{
			ID:          req.ID,
			Scope:       "user",
			OwnerUserID: req.UserID,
			Name:        req.Name,
			Description: req.Description,
		},
		Files:       req.Files,
		Origin:      origin,
		Status:      "published",
		ContentHash: skillbundle.ContentHash(req.Files),
		CreatedBy:   req.CreatedBy,
	}
	if rec.ID == "" {
		rec.ID = capabilitySlug("skill", req.Name) + "-" + randomSuffix()
		if err := s.store.CreateSkill(rec); err != nil {
			resp := nanoprotocol.NewErrorMessage("create skill: " + err.Error())
			return &resp
		}
	} else {
		existing, gerr := s.store.GetSkill(rec.ID)
		if gerr != nil || existing == nil || existing.Scope != "user" || existing.OwnerUserID != req.UserID {
			resp := nanoprotocol.NewErrorMessage("skill not found or not owned")
			return &resp
		}
		// Preserve provenance/versioning; user skills are overwritten in place.
		rec.Origin = existing.Origin
		rec.Version = existing.Version
		rec.PublishedVersion = existing.PublishedVersion
		if err := s.store.UpdateSkill(rec.ID, rec); err != nil {
			resp := nanoprotocol.NewErrorMessage("update skill: " + err.Error())
			return &resp
		}
	}
	s.emitCapabilitiesChanged("skill")
	slog.Info("skill saved", "id", rec.ID, "user_id", req.UserID, "origin", rec.Origin)

	stored, _ := s.store.GetSkill(rec.ID)
	def := skillDef(store.SkillRecord{})
	if stored != nil {
		def = skillDef(*stored)
	}
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSkillSaved, def)
	return &resp
}

// handleSkillDelete removes a user-scoped skill owned by the caller.
func (s *Server) handleSkillDelete(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SkillDeleteRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.UserID == "" {
		resp := nanoprotocol.NewErrorMessage("user_id is required")
		return &resp
	}
	recID := req.ID
	if recID == "" && req.Name != "" {
		found, ferr := s.store.GetUserSkillByName(req.UserID, req.Name)
		if ferr != nil || found == nil {
			resp := nanoprotocol.NewErrorMessage("skill not found or not owned")
			return &resp
		}
		recID = found.ID
	}
	if recID == "" {
		resp := nanoprotocol.NewErrorMessage("id or name is required")
		return &resp
	}
	existing, err := s.store.GetSkill(recID)
	if err != nil || existing == nil || existing.Scope != "user" || existing.OwnerUserID != req.UserID {
		resp := nanoprotocol.NewErrorMessage("skill not found or not owned")
		return &resp
	}
	if err := s.store.DeleteSkill(recID, req.UserID, "user"); err != nil {
		resp := nanoprotocol.NewErrorMessage("delete skill: " + err.Error())
		return &resp
	}
	s.emitCapabilitiesChanged("skill")
	resp, _ := nanoprotocol.NewMessage(nanoprotocol.TypeSkillDeleted, map[string]string{"id": recID})
	return &resp
}

// handleSkillAck records a user's acknowledgement of a platform skill version.
func (s *Server) handleSkillAck(msg nanoprotocol.Message) *nanoprotocol.Message {
	var req nanoprotocol.SkillAckRequest
	if err := msg.DecodePayload(&req); err != nil {
		resp := nanoprotocol.NewErrorMessage("invalid payload: " + err.Error())
		return &resp
	}
	if req.UserID == "" || req.SkillID == "" {
		resp := nanoprotocol.NewErrorMessage("user_id and skill_id are required")
		return &resp
	}
	if err := s.store.AckSkill(req.UserID, req.SkillID, req.Version); err != nil {
		resp := nanoprotocol.NewErrorMessage("ack skill: " + err.Error())
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
	for _, rule := range req.Permissions {
		if err := s.store.AppendModePermission(req.AgentID, typeID, rule); err != nil {
			resp := nanoprotocol.NewErrorMessage("set agent permission: " + err.Error())
			return &resp
		}
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
