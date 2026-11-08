package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// agentGroupPayload is the wire shape of a grouping. Position orders the groups;
// Agents is the order inside the group, which the arrange endpoint rewrites.
type agentGroupPayload struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Position int      `json:"position"`
	Agents   []string `json:"agents"`
}

type agentGroupRequest struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
}

type reorderAgentGroupsRequest struct {
	UserID string   `json:"user_id"`
	IDs    []string `json:"ids"`
}

type arrangeAgentGroupsRequest struct {
	UserID string `json:"user_id"`
	Groups []struct {
		ID     string   `json:"id"`
		Agents []string `json:"agents"`
	} `json:"groups"`
	Ungrouped []string `json:"ungrouped"`
}

func (s *Server) restListAgentGroups(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id query param is required"})
		return
	}

	groups, err := s.store.ListAgentGroups(userID)
	if err != nil {
		slog.Error("list agent groups", "user_id", userID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	order, ungrouped, err := s.store.ListAgentOrder(userID)
	if err != nil {
		slog.Error("list agent order", "user_id", userID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := make([]agentGroupPayload, 0, len(groups))
	for _, g := range groups {
		ids := order[g.ID]
		if ids == nil {
			ids = []string{}
		}
		out = append(out, agentGroupPayload{ID: g.ID, Name: g.Name, Position: g.Position, Agents: ids})
	}
	if ungrouped == nil {
		ungrouped = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out, "ungrouped": ungrouped, "count": len(out)})
}

func (s *Server) restCreateAgentGroup(w http.ResponseWriter, r *http.Request) {
	var req agentGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	group, err := s.store.CreateAgentGroup(req.UserID, req.Name)
	if err != nil {
		s.writeAgentGroupError(w, err, "create agent group")
		return
	}
	s.recordAudit(r, "agent_group.create", "agent_group", group.ID, map[string]any{"user_id": req.UserID})
	writeJSON(w, http.StatusCreated, agentGroupPayload{ID: group.ID, Name: group.Name, Position: group.Position})
}

func (s *Server) restRenameAgentGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req agentGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	if err := s.store.RenameAgentGroup(id, req.UserID, req.Name); err != nil {
		s.writeAgentGroupError(w, err, "rename agent group")
		return
	}
	s.recordAudit(r, "agent_group.rename", "agent_group", id, map[string]any{"user_id": req.UserID})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "updated"})
}

func (s *Server) restDeleteAgentGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id query param is required"})
		return
	}

	unassigned, err := s.store.DeleteAgentGroup(id, userID)
	if err != nil {
		s.writeAgentGroupError(w, err, "delete agent group")
		return
	}
	s.recordAudit(r, "agent_group.delete", "agent_group", id, map[string]any{
		"user_id": userID, "unassigned": unassigned,
	})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "deleted", "unassigned": unassigned})
}

func (s *Server) restReorderAgentGroups(w http.ResponseWriter, r *http.Request) {
	var req reorderAgentGroupsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}

	if err := s.store.ReorderAgentGroups(req.UserID, req.IDs); err != nil {
		s.writeAgentGroupError(w, err, "reorder agent groups")
		return
	}
	s.recordAudit(r, "agent_group.reorder", "agent_group", "", map[string]any{"user_id": req.UserID})
	writeJSON(w, http.StatusOK, map[string]any{"status": "reordered"})
}

// restArrangeAgentGroups writes the sidebar's order: which agent belongs to
// which group and where inside it. One request covers a drag across groups.
func (s *Server) restArrangeAgentGroups(w http.ResponseWriter, r *http.Request) {
	var req arrangeAgentGroupsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	groups := make(map[string][]string, len(req.Groups))
	for _, group := range req.Groups {
		if group.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "group id is required"})
			return
		}
		if _, ok := groups[group.ID]; ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "repeated group"})
			return
		}
		groups[group.ID] = group.Agents
	}

	if err := s.store.ArrangeAgents(req.UserID, groups, req.Ungrouped); err != nil {
		s.writeAgentGroupError(w, err, "arrange agents")
		return
	}
	s.recordAudit(r, "agent_group.arrange", "agent_group", "", map[string]any{
		"user_id": req.UserID, "groups": len(groups), "ungrouped": len(req.Ungrouped),
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "arranged"})
}

// writeAgentGroupError maps the store's sentinel errors onto status codes. A
// group owned by someone else surfaces as 404 everywhere (the caller has no
// business learning that the id exists).
func (s *Server) writeAgentGroupError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, store.ErrAgentGroupNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrAgentGroupNameTaken):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrAgentGroupLimit):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, store.ErrAgentGroupNameInvalid),
		errors.Is(err, store.ErrAgentGroupOrderInvalid),
		errors.Is(err, store.ErrAgentOrderInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		slog.Error(action, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}
