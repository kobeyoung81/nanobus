package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
)

// Bootstrap administrator
//
// A fresh installation has no administrator, and the console hides every
// administrative surface from a normal account. Someone has to be the first
// one; the rule this file implements is that the account created by the first
// sign-up takes the role. It is one-shot, atomic across concurrent
// registrations (see store.ClaimBootstrapAdmin) and switchable, because a
// deployment that opens registration to the internet must not hand its
// administrator role to whoever finds it first.

// firstUserIsAdmin reports the installation switch, defaulting to on. An
// installation seeded before the switch existed has no row for it, and the
// bootstrap rule is the behaviour it is meant to have.
func (s *Server) firstUserIsAdmin() bool {
	items, err := s.configItems(context.Background(), "auth")
	if err != nil {
		return true
	}
	for _, item := range items {
		if item.Key == "firstUserIsAdmin" {
			return strings.EqualFold(strings.TrimSpace(item.Value), "true")
		}
	}
	return true
}

// maybeBootstrapAdmin promotes the account a sign-up has just created while the
// installation still has no administrator. Failures are logged and swallowed:
// a registration must not fail because the optional role hand-over did.
func (s *Server) maybeBootstrapAdmin(r *http.Request, userID string) {
	if userID == "" || !s.firstUserIsAdmin() {
		return
	}
	admins, err := s.store.CountActiveAdmins()
	if err != nil {
		slog.Warn("bootstrap administrator: count administrators", "error", err)
		return
	}
	if admins > 0 {
		return
	}
	claimed, err := s.store.ClaimBootstrapAdmin(userID)
	if err != nil {
		slog.Error("bootstrap administrator: claim", "user_id", userID, "error", err)
		return
	}
	if !claimed {
		return
	}
	if err := s.store.SetUserRole(userID, "admin"); err != nil {
		// Give the claim back: this account is not an administrator, so the
		// next registration has to be able to take the role.
		if releaseErr := s.store.ReleaseBootstrapAdmin(userID); releaseErr != nil {
			slog.Error("bootstrap administrator: release", "user_id", userID, "error", releaseErr)
		}
		slog.Error("bootstrap administrator: set role", "user_id", userID, "error", err)
		return
	}
	slog.Info("bootstrap administrator", "user_id", userID,
		"hint", "the first account of this installation inherited the administrator role")
	s.recordAudit(r, "user.bootstrap.admin", "user", userID, nil)
}
