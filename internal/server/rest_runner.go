package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanoprotocol"
)

func (s *Server) restPollPendingContainers(w http.ResponseWriter, r *http.Request) {
	var req nanoprotocol.PollPendingContainersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	// Touch runner heartbeat on every poll (mirrors the WebSocket path).
	if req.RunnerID != "" {
		_ = s.store.UpdateRunnerHeartbeat(req.RunnerID)
	}

	requests, err := s.store.PollPendingContainers(req.RunnerID)
	if err != nil {
		slog.Error("poll pending containers", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, nanoprotocol.PollPendingContainersResponse{Requests: requests})
}

func (s *Server) restUpdateContainerStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req nanoprotocol.UpdateContainerStatusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if req.BindingID == 0 && req.UserID == "" {
		req.BindingID = 0
		// legacy path: the route id may be a user id
		req.UserID = id
	}

	if err := s.store.UpdateContainerStatus(req.UserID, req.BindingID, req.Status); err != nil {
		slog.Error("update container status", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) restRestartContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "container id is required"})
		return
	}

	if err := s.store.RestartContainerRequest(id); err != nil {
		slog.Error("restart container", "id", id, "error", err)
		if err == store.ErrContainerNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "restarting"})
}

// restReprovisionContainer revives a user's disposed/failed container request
// so the runner provisions a fresh container without requiring user traffic.
func (s *Server) restReprovisionContainer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "container id is required"})
		return
	}

	if err := s.store.ReprovisionContainerRequest(id); err != nil {
		slog.Error("reprovision container", "id", id, "error", err)
		if err == store.ErrContainerNotFound {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
}

func (s *Server) restRegisterRunner(w http.ResponseWriter, r *http.Request) {
	var req nanoprotocol.RegisterRunnerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := s.store.RegisterRunner(req.RunnerID, req.Hostname, req.DockerHost); err != nil {
		slog.Error("register runner", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, nanoprotocol.RegisterRunnerResponse{RunnerID: req.RunnerID})
}

func (s *Server) restDeleteRunner(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "runner id is required"})
		return
	}

	if err := s.store.DeleteRunner(id); err != nil {
		slog.Error("delete runner", "id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}
