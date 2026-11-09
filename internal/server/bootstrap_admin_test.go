package server

// The first account of a fresh installation takes the administrator role. The
// store's claim is tested on its own; this pins the wiring: a sign-up goes
// through the same endpoint the auth service calls, and the switch turns the
// behaviour off for deployments that expose registration.

import (
	"encoding/json"
	"net/http"
	"testing"
)

func registerAccount(t *testing.T, s *Server, id, email string) (int, string) {
	t.Helper()
	body := `{"id":"` + id + `","display_name":"` + id + `","email":"` + email + `","password_hash":"hash"}`
	return call(t, s, http.MethodPost, "/api/v1/users", body)
}

func accountRole(t *testing.T, s *Server, id string) string {
	t.Helper()
	code, body := call(t, s, http.MethodGet, "/api/v1/user-accounts/"+id, "")
	if code != http.StatusOK {
		t.Fatalf("read account %s: %d %s", id, code, body)
	}
	var account struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal([]byte(body), &account); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	return account.Role
}

func TestRestCreateUser_FirstAccountBecomesAdmin(t *testing.T) {
	s := testServerMongo(t)

	if code, body := registerAccount(t, s, "u-1", "first@example.test"); code != http.StatusCreated {
		t.Fatalf("register: %d %s", code, body)
	}
	if role := accountRole(t, s, "u-1"); role != "admin" {
		t.Fatalf("first account role = %q; want admin", role)
	}

	if code, body := registerAccount(t, s, "u-2", "second@example.test"); code != http.StatusCreated {
		t.Fatalf("second register: %d %s", code, body)
	}
	if role := accountRole(t, s, "u-2"); role != "user" {
		t.Fatalf("second account role = %q; want user", role)
	}
}

func TestRestCreateUser_SwitchOff(t *testing.T) {
	s := testServerMongo(t)
	if err := setConfigValue(t, s, "auth", "firstUserIsAdmin", "false"); err != nil {
		t.Fatalf("SetConfigItem: %v", err)
	}

	if code, body := registerAccount(t, s, "u-1", "first@example.test"); code != http.StatusCreated {
		t.Fatalf("register: %d %s", code, body)
	}
	if role := accountRole(t, s, "u-1"); role != "user" {
		t.Fatalf("first account role = %q; want user while the switch is off", role)
	}
}

func TestRestCreateUser_ExistingAdminIsNotReplaced(t *testing.T) {
	s := testServerMongo(t)
	if err := s.store.RegisterUser("root", "Root"); err != nil {
		t.Fatalf("RegisterUser: %v", err)
	}
	if err := s.store.SetUserRole("root", "admin"); err != nil {
		t.Fatalf("SetUserRole: %v", err)
	}

	if code, body := registerAccount(t, s, "u-1", "first@example.test"); code != http.StatusCreated {
		t.Fatalf("register: %d %s", code, body)
	}
	if role := accountRole(t, s, "u-1"); role != "user" {
		t.Fatalf("first sign-up role = %q; want user when an administrator already exists", role)
	}
}
