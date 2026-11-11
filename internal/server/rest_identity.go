package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/authclient"
	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// The three surfaces that used to be two other services' tables.
//
//   - oauth links: the auth service writes them (a directory identity is linked to
//     an account) and the console reads them (to show who signs in through the
//     directory);
//   - git shortcuts: the console's starred paths;
//   - refresh tokens: the auth service's session tickets.
//
// They are here because the bus is the only service with a database connection.
// The shapes are the callers': every field either service already used is still
// there under the same name, so the change on their side is a transport swap, not
// a remodel.

// oauthLinkJSON is one link as the callers see it.
func oauthLinkJSON(link store.OAuthLink) map[string]any {
	return map[string]any{
		"user_id":     link.UserID,
		"provider":    link.Provider,
		"provider_id": link.ProviderID,
	}
}

func (s *Server) restListOAuthLinks(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	links, err := s.store.OAuthLinks(userID)
	if err != nil {
		slog.Error("list oauth links", "user_id", userID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
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
	if providerID := r.URL.Query().Get("provider_id"); providerID != "" {
		userID, found, err := s.store.OAuthLinkOwner(provider, providerID)
		if err != nil {
			slog.Error("look up oauth identity", "provider", provider, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		ids := []string{}
		if found {
			ids = append(ids, userID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"user_ids": ids})
		return
	}
	links, err := s.store.OAuthProviderLinks(provider)
	if err != nil {
		slog.Error("list oauth links by provider", "provider", provider, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
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
	if err := s.store.LinkOAuth(userID, body.Provider, body.ProviderID); err != nil {
		slog.Error("link oauth", "user_id", userID, "provider", body.Provider, "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "linked"})
}

func (s *Server) restUnlinkOAuth(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	provider := r.PathValue("provider")
	if err := s.store.UnlinkOAuth(userID, provider); err != nil {
		slog.Error("unlink oauth", "user_id", userID, "provider", provider, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
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

// restSaveRefreshToken stores a session ticket. The hash is what the client
// presented to the auth service; the bus never sees the token itself.
func (s *Server) restSaveRefreshToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TokenHash string `json:"token_hash"`
		UserID    string `json:"user_id"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expires_at must be RFC3339"})
		return
	}
	if err := s.store.SaveRefreshToken(body.TokenHash, body.UserID, expiresAt); err != nil {
		slog.Error("save refresh token", "user_id", body.UserID, "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved"})
}

func (s *Server) restGetRefreshToken(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	userID, expiresAt, found, err := s.store.RefreshToken(hash)
	if err != nil {
		slog.Error("read refresh token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "refresh token not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":    userID,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *Server) restDeleteRefreshToken(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if err := s.store.DeleteRefreshToken(hash); err != nil {
		slog.Error("delete refresh token", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

// restRevokeUserRefreshTokens ends every session a user holds — what a password
// change or a suspension has to do.
func (s *Server) restRevokeUserRefreshTokens(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if err := s.store.DeleteUserRefreshTokens(userID); err != nil {
		slog.Error("revoke user refresh tokens", "user_id", userID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

// restPurgeRefreshTokens drops the tickets that have expired. The auth service
// calls it on the paths that already look at tokens; nothing schedules it.
func (s *Server) restPurgeRefreshTokens(w http.ResponseWriter, r *http.Request) {
	deleted, err := s.store.PurgeExpiredRefreshTokens(time.Now().UTC())
	if err != nil {
		slog.Error("purge refresh tokens", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// errUserNotFound is the sentinel the account endpoints answer 404 on.
var errUserNotFound = errors.New("user not found")
