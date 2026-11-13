package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/provisionerclient"
	"gitee.com/kobeyoung81/nanoprotocol"
)

// The provisioning queue belongs to the runner: it polls it and records what
// happened. The bus keeps the console's paths and forwards the actions, the same
// shape configuration has (bus → admin). What is left here are the reads the
// console needs and the runner registry.

func (s *Server) restRegisterRunner(w http.ResponseWriter, r *http.Request) {
	var req nanoprotocol.RegisterRunnerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if err := s.callProvisioner(r, func(ctx context.Context, c *provisionerclient.Client) error {
		return c.RegisterRunner(ctx, req.RunnerID, req.Hostname, req.DockerHost)
	}); err != nil {
		slog.Error("register runner", "runner_id", req.RunnerID, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "provisioner unavailable"})
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

	if err := s.callProvisioner(r, func(ctx context.Context, c *provisionerclient.Client) error {
		return c.DeleteRunner(ctx, id)
	}); err != nil {
		slog.Error("delete runner", "id", id, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "provisioner unavailable"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}

// forwardRunnerRegistration hands a runner's self-registration to the registry's
// owner. Runners that predate the move still call this service's RPC.
func (s *Server) forwardRunnerRegistration(req nanoprotocol.RegisterRunnerRequest) error {
	if s.provisioner == nil || !s.provisioner.Enabled() {
		return fmt.Errorf("the provisioner module is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return s.provisioner.RegisterRunner(ctx, req.RunnerID, req.Hostname, req.DockerHost)
}

// callProvisioner runs one forwarded action against the runner's API.
func (s *Server) callProvisioner(r *http.Request, fn func(ctx context.Context, c *provisionerclient.Client) error) error {
	if s.provisioner == nil || !s.provisioner.Enabled() {
		return fmt.Errorf("the provisioner module is not configured")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	return fn(ctx, s.provisioner)
}

// writeProvisionerError passes the runner's own status through where it means
// something (404 for "nothing to act on"), and reports anything else as a gateway
// failure: the caller asked a service that could not be reached or failed.
func (s *Server) writeProvisionerError(w http.ResponseWriter, err error, action, id string) {
	switch provisionerclient.CodeOf(err) {
	case http.StatusNotFound:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "container request not found"})
	case http.StatusConflict:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "container already requested"})
	default:
		slog.Error(action, "id", id, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "provisioner unavailable"})
	}
}
