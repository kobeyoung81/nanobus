package server

import (
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

func (s *Server) restGetUser(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")

	status, level, err := s.store.GetUserInfo(userID)
	if err != nil {
		if err == store.ErrUserNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		slog.Error("get user info", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"user_id": userID,
		"status":  status,
		"tier":    level,
	})
}
