package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// The user administration surface, served for the portal.
//
// These endpoints exist because `users` was a table two services read: the portal
// administered an account's identity (role, status, tier, password) straight out
// of MySQL while the bus read the same row for its own three fields. Once the bus
// owned the storage, those two halves could disagree — so the portal asks here.
//
// The shapes are the portal's, not the bus's: an account is returned the way the
// console shows it, including the channels it is reachable on. What the portal
// adds on top (whether an account signs in through the directory) comes from its
// own OAuth table, which is not the bus's business.

// userAccountJSON is one account as the console shows it. The credential is
// deliberately absent: only the hash endpoint returns it, and only to a caller
// that presents the internal token.
//
// `ldap` says the account signs in through the directory, which the console used
// to work out by joining its own oauth table. It comes from the links the bus
// owns now (see rest_identity.go), so the console does not need that table.
func userAccountJSON(sum store.UserSummary, channels []string, ldap bool) map[string]any {
	return map[string]any{
		"user_id":              sum.ID,
		"display_name":         sum.DisplayName,
		"email":                sum.Email,
		"status":               sum.Status,
		"tier":                 sum.Tier,
		"role":                 sum.Role,
		"channels":             channels,
		"must_change_password": sum.MustChangePassword,
		"ldap":                 ldap,
		"updated_at":           sum.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// directoryLinkedUserIDs is the set of accounts that sign in through the
// directory, for a whole page of the list in one query rather than one per row.
func (s *Server) directoryLinkedUserIDs() (map[string]bool, error) {
	links, err := s.auth.OAuthLinksByProvider(context.Background(), "ldap")
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(links))
	for _, link := range links {
		out[link.UserID] = true
	}
	return out, nil
}

// userHasDirectoryLink answers the same question for a single account.
func (s *Server) userHasDirectoryLink(userID string) (bool, error) {
	links, err := s.auth.OAuthLinks(context.Background(), userID)
	if err != nil {
		return false, err
	}
	for _, link := range links {
		if link.Provider == "ldap" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) restListUserAccounts(w http.ResponseWriter, r *http.Request) {
	includeDeleted := r.URL.Query().Get("include_deleted") == "1" || r.URL.Query().Get("include_deleted") == "true"
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	// A member picker starts from a fragment, not an address. It gets a short
	// list of identities and nothing else: the credential and the channels are
	// the console's business, not a share dialog's.
	if q := r.URL.Query().Get("q"); q != "" {
		sums, err := s.store.SearchUserSummaries(q, limit)
		if err != nil {
			slog.Error("search user accounts", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		users := make([]map[string]any, 0, len(sums))
		for _, sum := range sums {
			users = append(users, map[string]any{
				"user_id":      sum.ID,
				"display_name": sum.DisplayName,
				"email":        sum.Email,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": users})
		return
	}

	// The sign-in path starts from an address, not an id. It asks for exactly one
	// account and gets a list of zero or one, so it can use the same client call.
	if email := r.URL.Query().Get("email"); email != "" {
		sum, err := s.store.FindUserByEmail(email)
		switch {
		case errors.Is(err, store.ErrUserNotFound):
			writeJSON(w, http.StatusOK, map[string]any{"users": []any{}})
			return
		case err != nil:
			slog.Error("find user by email", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		channels, err := s.store.UserChannels(sum.ID)
		if err != nil {
			slog.Error("read user channels", "user_id", sum.ID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		ldap, err := s.userHasDirectoryLink(sum.ID)
		if err != nil {
			slog.Error("read oauth link", "user_id", sum.ID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": []any{userAccountJSON(*sum, channels, ldap)}})
		return
	}

	summaries, err := s.store.ListUserSummaries(includeDeleted, limit)
	if err != nil {
		slog.Error("list user accounts", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	directory, err := s.directoryLinkedUserIDs()
	if err != nil {
		slog.Error("list directory links", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	out := make([]map[string]any, 0, len(summaries))
	for _, sum := range summaries {
		channels, err := s.store.UserChannels(sum.ID)
		if err != nil {
			slog.Error("read user channels", "user_id", sum.ID, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		out = append(out, userAccountJSON(sum, channels, directory[sum.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// restCountUserAccounts answers the overview's headline numbers with counts
// rather than a list, so the console does not fetch every row to print a number.
func (s *Server) restCountUserAccounts(w http.ResponseWriter, r *http.Request) {
	active, err := s.store.CountActiveUsers()
	if err != nil {
		slog.Error("count active users", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	admins, err := s.store.CountActiveAdmins()
	if err != nil {
		slog.Error("count active admins", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	tiers, err := s.store.CountUsersByTier()
	if err != nil {
		slog.Error("count users by tier", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"active": active, "admins": admins, "tiers": tiers})
}

func (s *Server) restGetUserAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("userID")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user id is required"})
		return
	}

	sum, err := s.store.GetUserAccount(id)
	if err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		slog.Error("read user account", "user_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	channels, err := s.store.UserChannels(id)
	if err != nil {
		slog.Error("read user channels", "user_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	ldap, err := s.userHasDirectoryLink(id)
	if err != nil {
		slog.Error("read oauth link", "user_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, userAccountJSON(*sum, channels, ldap))
}

// restSetUserAccount applies a partial update: only the fields the body names.
func (s *Server) restSetUserAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("userID")
	var body struct {
		Role        *string `json:"role,omitempty"`
		Status      *string `json:"status,omitempty"`
		Tier        *string `json:"tier,omitempty"`
		DisplayName *string `json:"display_name,omitempty"`
		Email       *string `json:"email,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	type change struct {
		what  string
		apply func() error
	}
	changes := []change{}
	if body.Role != nil {
		changes = append(changes, change{"role", func() error { return s.store.SetUserRole(id, *body.Role) }})
	}
	if body.Status != nil {
		changes = append(changes, change{"status", func() error { return s.store.UpdateUserStatus(id, *body.Status) }})
	}
	if body.Tier != nil {
		changes = append(changes, change{"tier", func() error { return s.store.SetUserTier(id, *body.Tier) }})
	}
	if body.DisplayName != nil {
		changes = append(changes, change{"display_name", func() error { return s.store.SetUserDisplayName(id, *body.DisplayName) }})
	}
	if body.Email != nil {
		changes = append(changes, change{"email", func() error { return s.store.SetUserEmail(id, *body.Email) }})
	}
	if len(changes) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "nothing to update"})
		return
	}

	for _, c := range changes {
		if err := c.apply(); err != nil {
			if errors.Is(err, store.ErrEmailTaken) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "email already in use"})
				return
			}
			if errors.Is(err, store.ErrUserNotFound) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
				return
			}
			slog.Error("update user account", "user_id", id, "field", c.what, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": id, "status": "updated"})
}

// restGetUserPasswordHash returns an account's stored hash.
//
// It is here because the *verification* stays in the portal: that is where the
// login form is, and bcrypt is a check, not a service. Sending the hash over the
// internal (token-authenticated) API is the smaller of the two costs — the
// alternative is sending the password itself, which would mean every service in
// between could read it.
func (s *Server) restGetUserPasswordHash(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("userID")
	hash, err := s.store.UserPasswordHash(id)
	if err != nil {
		slog.Error("read user password hash", "user_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if hash == "" {
		exists, err := s.store.UserExists(id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
			return
		}
		if !exists {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": id, "password_hash": hash})
}

// restSetUserPassword stores a hash the caller produced.
func (s *Server) restSetUserPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("userID")
	var body struct {
		PasswordHash string `json:"password_hash"`
		// Omitted means "leave the flag as it is": the directory path wipes a
		// password and must not also decide whether the next one is forced.
		MustChange *bool `json:"must_change_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.PasswordHash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "password_hash is required"})
		return
	}

	var setErr error
	if body.MustChange == nil {
		setErr = s.store.SetUserPasswordKeepingFlag(id, body.PasswordHash)
	} else {
		setErr = s.store.SetUserPassword(id, body.PasswordHash, *body.MustChange)
	}
	if err := setErr; err != nil {
		if errors.Is(err, store.ErrUserNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		slog.Error("set user password", "user_id", id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": id, "status": "updated"})
}
