package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// ======== user: skill acknowledgement ========

type skillAckBody struct {
	SkillID string `json:"skill_id"`
	Version int    `json:"version"`
}

func (s *Server) restUserSkillAck(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	var body skillAckBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.SkillID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill_id is required"})
		return
	}
	if err := s.store.AckSkill(userID, body.SkillID, body.Version); err != nil {
		slog.Error("ack skill", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.recordAudit(r, "capabilities.skill.ack", "skill", body.SkillID, map[string]any{"user_id": userID, "version": body.Version})
	writeJSON(w, http.StatusOK, map[string]any{"skill_id": body.SkillID, "version": body.Version})
}

// ======== user: secrets ========

// restListUserSecrets returns the secret keys for a capability with masked
// values (never the plaintext).
func (s *Server) restListUserSecrets(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	kind := r.PathValue("kind")
	ref := r.PathValue("ref")
	vals, err := s.store.ListUserSecrets(userID, kind, ref)
	if err != nil {
		slog.Error("list user secrets", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	masked := map[string]string{}
	for k := range vals {
		masked[k] = maskValue
	}
	writeJSON(w, http.StatusOK, map[string]any{"secrets": masked, "count": len(masked)})
}

type secretBody struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (s *Server) restSetUserSecret(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	kind := r.PathValue("kind")
	ref := r.PathValue("ref")
	var body secretBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if body.Key == "" || body.Value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key and value are required"})
		return
	}
	if !validSecretKind(kind) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid capability kind"})
		return
	}
	if err := s.store.UpsertUserSecret(userID, kind, ref, body.Key, body.Value); err != nil {
		slog.Error("set user secret", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.recordAudit(r, "capabilities.secret.set", kind, ref, map[string]any{"user_id": userID, "key": body.Key})
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) restDeleteUserSecret(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromQuery(r)
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	kind := r.PathValue("kind")
	ref := r.PathValue("ref")
	key := r.URL.Query().Get("key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key is required"})
		return
	}
	if err := s.store.DeleteUserSecret(userID, kind, ref, key); err != nil {
		slog.Error("delete user secret", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	s.recordAudit(r, "capabilities.secret.delete", kind, ref, map[string]any{"user_id": userID, "key": key})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func validSecretKind(kind string) bool {
	switch kind {
	case "skill", "mcp", "model":
		return true
	}
	return false
}
