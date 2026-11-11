package store_test

// The account fields the console owns: reading an account back by address, and
// renaming it. The identity links moved to nanoauth with the rest of identity, and
// their tests with them.

import (
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func TestFindUserByEmailAndRename(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.RegisterUser("u-1", "Kobe"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.SetUserEmail("u-1", "kobeyoung81@sina.com"); err != nil {
		t.Fatalf("set email: %v", err)
	}

	sum, err := s.FindUserByEmail("kobeyoung81@sina.com")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if sum.ID != "u-1" || sum.Email != "kobeyoung81@sina.com" {
		t.Fatalf("unexpected summary: %+v", sum)
	}
	if _, err := s.FindUserByEmail("nobody@example.com"); err == nil {
		t.Fatal("an unknown address must report not-found")
	}

	if err := s.SetUserDisplayName("u-1", "Renamed"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if sum, err = s.GetUserAccount("u-1"); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if sum.DisplayName != "Renamed" {
		t.Fatalf("rename did not stick: %+v", sum)
	}
	if err := s.SetUserDisplayName("ghost", "x"); err == nil {
		t.Fatal("renaming an unknown account must fail")
	}
}

// TestSetUserPasswordKeepingFlag_LeavesTheFlagAlone is the directory path's
// requirement: wiping a local password must not also decide whether the next one
// is forced.
func TestSetUserPasswordKeepingFlag_LeavesTheFlagAlone(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.RegisterUser("u-1", "Kobe"); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := s.SetUserPassword("u-1", "first-hash", true); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if sum, err := s.GetUserAccount("u-1"); err != nil || !sum.MustChangePassword {
		t.Fatalf("the forced change did not stick: %+v err=%v", sum, err)
	}

	// The directory path blanks the password; the flag's value is not its business.
	if err := s.SetUserPasswordKeepingFlag("u-1", ""); err != nil {
		t.Fatalf("keep-flag update: %v", err)
	}
	sum, err := s.GetUserAccount("u-1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !sum.MustChangePassword {
		t.Fatal("the keep-flag update reset the flag")
	}
	if hash, err := s.UserPasswordHash("u-1"); err != nil || hash != "" {
		t.Fatalf("the password was not blanked: %q err=%v", hash, err)
	}

	// And the explicit form still sets it.
	if err := s.SetUserPassword("u-1", "second-hash", false); err != nil {
		t.Fatalf("explicit update: %v", err)
	}
	if sum, err = s.GetUserAccount("u-1"); err != nil || sum.MustChangePassword {
		t.Fatalf("the explicit flag was ignored: %+v err=%v", sum, err)
	}
}
