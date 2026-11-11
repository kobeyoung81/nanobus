package store_test

// The directory links. What has to hold is what the callers relied on: a link is
// one row per provider identity, and re-linking an identity moves it rather than
// adding a second owner. The starred paths and the session tickets moved to
// nanoauth with the rest of identity, and their tests with them.

import (
	"encoding/json"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	migrationsmongo "gitee.com/kobeyoung81/nanobus/migrations-mongo"
)

// TestLinkOAuth_IdentityBelongsToOneAccount pins the source table's composite
// primary key: an identity is one row, and re-linking it moves it rather than
// adding a second owner.
func TestLinkOAuth_IdentityBelongsToOneAccount(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.LinkOAuth("u-1", "ldap", "cn=kobeyoung,ou=people"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := s.LinkOAuth("u-1", "ldap", "cn=kobeyoung,ou=people"); err != nil {
		t.Fatalf("re-link: %v", err)
	}

	links, err := s.OAuthLinks("u-1")
	if err != nil {
		t.Fatalf("links: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("re-linking produced %d rows: %+v", len(links), links)
	}
	if links[0].Provider != "ldap" || links[0].ProviderID != "cn=kobeyoung,ou=people" {
		t.Fatalf("unexpected link: %+v", links[0])
	}

	if err := s.LinkOAuth("u-2", "ldap", "cn=kobeyoung,ou=people"); err != nil {
		t.Fatalf("relink to another account: %v", err)
	}
	u1, err := s.OAuthLinks("u-1")
	if err != nil {
		t.Fatalf("links u-1: %v", err)
	}
	u2, err := s.OAuthLinks("u-2")
	if err != nil {
		t.Fatalf("links u-2: %v", err)
	}
	if len(u1) != 0 || len(u2) != 1 {
		t.Fatalf("the identity did not move: u-1=%+v u-2=%+v", u1, u2)
	}
}

// TestOAuthProviderLinks_IsTheDirectoryRoster is what the console's user list
// reads instead of its own DISTINCT query.
func TestOAuthProviderLinks_IsTheDirectoryRoster(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.LinkOAuth("u-1", "ldap", "cn=one"); err != nil {
		t.Fatalf("link one: %v", err)
	}
	if err := s.LinkOAuth("u-2", "ldap", "cn=two"); err != nil {
		t.Fatalf("link two: %v", err)
	}
	if err := s.LinkOAuth("u-3", "github", "gh-3"); err != nil {
		t.Fatalf("link github: %v", err)
	}

	links, err := s.OAuthProviderLinks("ldap")
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("expected two directory accounts, got %+v", links)
	}
	for _, link := range links {
		if link.Provider != "ldap" {
			t.Fatalf("the roster leaked another provider: %+v", link)
		}
	}

	if err := s.UnlinkOAuth("u-1", "ldap"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if links, err = s.OAuthProviderLinks("ldap"); err != nil {
		t.Fatalf("roster after unlink: %v", err)
	} else if len(links) != 1 {
		t.Fatalf("unlink did not take effect: %+v", links)
	}
}

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

// TestIdentityCollectionsAreMigrated: the collections still written here have to
// be in the index manifest, or a fresh install would run without their
// uniqueness. `auth_git_shortcuts` moved with its writer and is declared by
// nanoauth now.
func TestIdentityCollectionsAreMigrated(t *testing.T) {
	manifests, err := migrationsmongo.Load()
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	wanted := map[string]bool{
		"auth_oauth_providers": false,
	}
	for name, raw := range manifests {
		var manifest struct {
			Collections map[string]json.RawMessage `json:"collections"`
		}
		if err := json.Unmarshal([]byte(raw), &manifest); err != nil {
			t.Fatalf("decode manifest %s: %v", name, err)
		}
		for collection := range manifest.Collections {
			if _, ok := wanted[collection]; ok {
				wanted[collection] = true
			}
		}
	}
	for collection, found := range wanted {
		if !found {
			t.Errorf("%s has no index manifest", collection)
		}
	}
}

// TestOAuthLinkOwner_AnswersTheSignInQuestion: given a directory/OAuth identity,
// which account is it? The pair is unique, so this is one lookup.
func TestOAuthLinkOwner_AnswersTheSignInQuestion(t *testing.T) {
	s := storetest.Connect(t)

	if err := s.LinkOAuth("u-1", "wechat", "openid-9"); err != nil {
		t.Fatalf("link: %v", err)
	}
	userID, found, err := s.OAuthLinkOwner("wechat", "openid-9")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if !found || userID != "u-1" {
		t.Fatalf("lookup gave %q found=%v", userID, found)
	}
	if _, found, err := s.OAuthLinkOwner("wechat", "openid-unknown"); err != nil || found {
		t.Fatalf("an unknown identity must not resolve: found=%v err=%v", found, err)
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
