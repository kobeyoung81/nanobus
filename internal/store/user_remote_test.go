package store_test

// The bus no longer keeps `auth_users`: the account methods ask the auth module. What
// has to hold at this layer is that "no backend wired" is an error rather than an
// empty answer, because an empty answer looks like "nobody has an account".

import (
	"strings"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func TestAccountMethodsRefuseWithoutABackend(t *testing.T) {
	s := storetest.Connect(t)

	if _, err := s.CountActiveUsers(); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("CountActiveUsers = %v, want a not-configured error", err)
	}
	if _, _, err := s.GetUserInfo("u-1"); err == nil {
		t.Fatal("GetUserInfo answered without a backend")
	}
	if err := s.SetUserRole("u-1", "admin"); err == nil {
		t.Fatal("a field write was accepted without a backend")
	}
	if _, err := s.ResolveUserBinding("web", "sender-1"); err == nil {
		t.Fatal("onboarding was accepted without a backend")
	}
}
