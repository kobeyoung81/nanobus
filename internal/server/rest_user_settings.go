package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
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
	// The catalogue (and its acknowledgements) is the admin module's.
	if err := s.adminPost("/api/v1/internal/skills/ack", map[string]any{
		"user_id": userID, "skill_id": body.SkillID, "version": body.Version,
	}, nil); err != nil {
		slog.Error("ack skill", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "catalogue unavailable"})
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
	// The rows are identity's; the masking below stays here, because it is the
	// browser's view of them.
	vals, err := s.auth.UserSecrets(r.Context(), userID, kind, ref)
	if err != nil {
		slog.Error("list user secrets", "error", err)
		writeAuthFailure(w, err)
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
	if err := s.auth.SetUserSecret(r.Context(), userID, kind, ref, body.Key, body.Value); err != nil {
		slog.Error("set user secret", "error", err)
		writeAuthFailure(w, err)
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
	if err := s.auth.DeleteUserSecret(r.Context(), userID, kind, ref, key); err != nil {
		slog.Error("delete user secret", "error", err)
		writeAuthFailure(w, err)
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

// writeAuthFailure answers with the identity module's own status when it rejected
// the call (a 404 for an account that is not there, a 409 for a taken address), and
// with 502 when it could not be reached: the caller turns the first into "not
// found" and the second into "something is wrong".
func writeAuthFailure(w http.ResponseWriter, err error) {
	var rejected *authclient.StatusError
	if errors.As(err, &rejected) && rejected.Status < http.StatusInternalServerError {
		writeJSON(w, rejected.Status, map[string]string{"error": rejected.Body})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the auth module is unavailable"})
}
