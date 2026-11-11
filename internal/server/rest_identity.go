package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
)

// The identity surfaces the console and the sign-in flow call.
//
//   - oauth links: the auth service writes them (a directory identity is linked to
//     an account) and the console reads them (who signs in through the directory);
//   - git shortcuts: the console's starred paths.
//
// The rows are identity's; these handlers keep the shapes their callers already
// use and forward. The refresh tickets are not here at all any more — they are the
// auth service's own state and it keeps them itself.

// oauthLinkJSON is one link as the callers see it.
func oauthLinkJSON(link authclient.OAuthLink) map[string]any {
	return map[string]any{
		"user_id":     link.UserID,
		"provider":    link.Provider,
		"provider_id": link.ProviderID,
	}
}

func (s *Server) restListOAuthLinks(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	links, err := s.auth.OAuthLinks(r.Context(), userID)
	if err != nil {
		slog.Error("list oauth links", "user_id", userID, "error", err)
		writeAuthFailure(w, err)
		return
	}
	out := make([]map[string]any, 0, len(links))
	for _, link := range links {
		out = append(out, oauthLinkJSON(link))
	}
	writeJSON(w, http.StatusOK, map[string]any{"links": out})
}

// restListOAuthLinksByProvider answers "who signs in as one provider" in one call:
// the console's user list used to run a DISTINCT query per page.
func (s *Server) restListOAuthLinksByProvider(w http.ResponseWriter, r *http.Request) {
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider is required"})
		return
	}
	// `provider_id` narrows it to one identity, which is how the sign-in flow asks
	// "which account is this directory/OAuth subject".
	links, err := s.auth.OAuthLinksByProvider(r.Context(), provider)
	if err != nil {
		slog.Error("list oauth links by provider", "provider", provider, "error", err)
		writeAuthFailure(w, err)
		return
	}
	if providerID := r.URL.Query().Get("provider_id"); providerID != "" {
		ids := []string{}
		for _, link := range links {
			if link.ProviderID == providerID {
				ids = append(ids, link.UserID)
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"user_ids": ids})
		return
	}
	seen := map[string]bool{}
	userIDs := make([]string, 0, len(links))
	for _, link := range links {
		if !seen[link.UserID] {
			seen[link.UserID] = true
			userIDs = append(userIDs, link.UserID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_ids": userIDs})
}

func (s *Server) restLinkOAuth(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	var body struct {
		Provider   string `json:"provider"`
		ProviderID string `json:"provider_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if err := s.auth.LinkOAuth(r.Context(), userID, body.Provider, body.ProviderID); err != nil {
		slog.Error("link oauth", "user_id", userID, "provider", body.Provider, "error", err)
		writeAuthFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "linked"})
}

func (s *Server) restUnlinkOAuth(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	provider := r.PathValue("provider")
	if err := s.auth.UnlinkOAuth(r.Context(), userID, provider); err != nil {
		slog.Error("unlink oauth", "user_id", userID, "provider", provider, "error", err)
		writeAuthFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "unlinked"})
}

// A user's starred git paths are identity's, so nanoauth owns the rows; these
// three handlers keep the console's endpoints and forward them.

func (s *Server) restListGitShortcuts(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	shortcuts, err := s.auth.GitShortcuts(r.Context(), userID, r.URL.Query().Get("agent_id"))
	if err != nil {
		slog.Error("list git shortcuts", "user_id", userID, "error", err)
		writeAuthFailure(w, err)
		return
	}
	out := make([]map[string]any, 0, len(shortcuts))
	for _, shortcut := range shortcuts {
		out = append(out, map[string]any{
			"id":       shortcut.ID,
			"user_id":  shortcut.UserID,
			"agent_id": shortcut.AgentID,
			"path":     shortcut.Path,
			"label":    shortcut.Label,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"shortcuts": out})
}

func (s *Server) restAddGitShortcut(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	var body struct {
		AgentID string `json:"agent_id"`
		Path    string `json:"path"`
		Label   string `json:"label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	shortcut, err := s.auth.SaveGitShortcut(r.Context(), userID, body.AgentID, body.Path, body.Label)
	if err != nil {
		slog.Error("save git shortcut", "user_id", userID, "error", err)
		writeAuthFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       shortcut.ID,
		"user_id":  shortcut.UserID,
		"agent_id": shortcut.AgentID,
		"path":     shortcut.Path,
		"label":    shortcut.Label,
	})
}

func (s *Server) restDeleteGitShortcut(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id must be a number"})
		return
	}
	if err := s.auth.DeleteGitShortcut(r.Context(), userID, id); err != nil {
		if errors.Is(err, authclient.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("shortcut %d not found", id)})
			return
		}
		slog.Error("delete git shortcut", "user_id", userID, "error", err)
		writeAuthFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// writeAuthFailure answers with nanoauth's own rejection when the request was the
// problem, and reports an unavailable auth module otherwise.
func writeAuthFailure(w http.ResponseWriter, err error) {
	var rejected *authclient.StatusError
	if errors.As(err, &rejected) && rejected.Status < http.StatusInternalServerError {
		writeJSON(w, rejected.Status, map[string]string{"error": rejected.Body})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": "the auth module is unavailable"})
}

// errUserNotFound is the sentinel the account endpoints answer 404 on.
var errUserNotFound = errors.New("user not found")
