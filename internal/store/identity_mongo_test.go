package store_test

// The three domains that used to be somebody else's tables. What has to hold is
// what the callers relied on: a star is one row per (user, agent, path) with a
// stable id, a link is one row per provider identity, and a refresh token is
// addressed by its own hash.

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
	migrationsmongo "gitee.com/kobeyoung81/nanobus/migrations-mongo"
)

func TestSaveGitShortcut_StarsThenRelabels(t *testing.T) {
	s := storetest.Connect(t)

	first, err := s.SaveGitShortcut("u-1", "agent-1", "src/main.go", "main")
	if err != nil {
		t.Fatalf("first star: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("a new star got no id")
	}

	// Re-starring the same path is the same row with a new label — the console
	// shows one entry, not two.
	second, err := s.SaveGitShortcut("u-1", "agent-1", "src/main.go", "entry point")
	if err != nil {
		t.Fatalf("re-star: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("re-starring created a second row: %d then %d", first.ID, second.ID)
	}
	if second.Label != "entry point" {
		t.Fatalf("label = %q, want the new one", second.Label)
	}

	rows, err := s.GitShortcuts("u-1", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected one starred path, got %+v", rows)
	}
	if rows[0].Label != "entry point" {
		t.Fatalf("the list did not see the new label: %+v", rows[0])
	}
}

// TestSaveGitShortcut_ConcurrentStarsCollapse is the guard for the unique index
// on (user_id, agent_id, path): eight tabs starring the same file at once must
// converge on one row, and every caller must come away with the same id — the
// console deletes by that id.
func TestSaveGitShortcut_ConcurrentStarsCollapse(t *testing.T) {
	s := storetest.Connect(t)

	var wg sync.WaitGroup
	ids := make([]int64, 8)
	errs := make([]error, 8)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			shortcut, err := s.SaveGitShortcut("u-1", "agent-1", "README.md", "label")
			ids[i], errs[i] = shortcut.ID, err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("star %d: %v", i, err)
		}
	}
	for i, id := range ids {
		if id != ids[0] {
			t.Fatalf("star %d got id %d, the first got %d", i, id, ids[0])
		}
	}
	rows, err := s.GitShortcuts("u-1", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("eight concurrent stars produced %d rows: %+v", len(rows), rows)
	}
}

func TestGitShortcuts_AreScopedAndOrdered(t *testing.T) {
	s := storetest.Connect(t)

	if _, err := s.SaveGitShortcut("u-1", "agent-1", "a.go", ""); err != nil {
		t.Fatalf("star a: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // created_at is the sort key; ids break ties
	if _, err := s.SaveGitShortcut("u-1", "agent-1", "b.go", ""); err != nil {
		t.Fatalf("star b: %v", err)
	}
	if _, err := s.SaveGitShortcut("u-1", "agent-2", "c.go", ""); err != nil {
		t.Fatalf("star c: %v", err)
	}

	all, err := s.GitShortcuts("u-1", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected three stars, got %+v", all)
	}
	// c.go was starred last, so it heads the list: newest first.
	if all[0].Path != "c.go" || all[2].Path != "a.go" {
		t.Fatalf("newest first expected, got %+v", all)
	}
	scoped, err := s.GitShortcuts("u-1", "agent-2")
	if err != nil {
		t.Fatalf("list scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].Path != "c.go" {
		t.Fatalf("agent filter leaked: %+v", scoped)
	}
	other, err := s.GitShortcuts("u-2", "")
	if err != nil {
		t.Fatalf("list other user: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("another user saw rows: %+v", other)
	}
}

func TestDeleteGitShortcut_UnknownIsAnError(t *testing.T) {
	s := storetest.Connect(t)

	shortcut, err := s.SaveGitShortcut("u-1", "agent-1", "a.go", "")
	if err != nil {
		t.Fatalf("star: %v", err)
	}
	if err := s.DeleteGitShortcut("u-1", shortcut.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteGitShortcut("u-1", shortcut.ID); err == nil {
		t.Fatal("deleting it twice must report not-found, as it did in SQL")
	}
	// Another user cannot delete it either.
	again, err := s.SaveGitShortcut("u-1", "agent-1", "a.go", "")
	if err != nil {
		t.Fatalf("restar: %v", err)
	}
	if err := s.DeleteGitShortcut("u-2", again.ID); err == nil {
		t.Fatal("a stranger deleted somebody's star")
	}
}

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

func TestRefreshToken_Lifecycle(t *testing.T) {
	s := storetest.Connect(t)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)

	if err := s.SaveRefreshToken("hash-1", "u-1", expires); err != nil {
		t.Fatalf("save: %v", err)
	}
	userID, got, found, err := s.RefreshToken("hash-1")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !found || userID != "u-1" || !got.Equal(expires) {
		t.Fatalf("read gave %q %v found=%v", userID, got, found)
	}

	// Saving the same hash again is the same ticket, not a second one.
	if err := s.SaveRefreshToken("hash-1", "u-1", expires); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if err := s.SaveRefreshToken("hash-2", "u-2", expires); err != nil {
		t.Fatalf("second ticket: %v", err)
	}

	if err := s.DeleteRefreshToken("hash-1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, _, found, err := s.RefreshToken("hash-1"); err != nil || found {
		t.Fatalf("revoked ticket still found: found=%v err=%v", found, err)
	}
	// Logout must be safe to repeat.
	if err := s.DeleteRefreshToken("hash-1"); err != nil {
		t.Fatalf("revoking twice errored: %v", err)
	}

	if err := s.DeleteUserRefreshTokens("u-2"); err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if _, _, found, err := s.RefreshToken("hash-2"); err != nil || found {
		t.Fatalf("user revocation missed the ticket: found=%v err=%v", found, err)
	}
}

func TestPurgeExpiredRefreshTokens_KeepsLiveOnes(t *testing.T) {
	s := storetest.Connect(t)
	now := time.Now().UTC()

	if err := s.SaveRefreshToken("expired", "u-1", now.Add(-time.Hour)); err != nil {
		t.Fatalf("save expired: %v", err)
	}
	if err := s.SaveRefreshToken("live", "u-1", now.Add(time.Hour)); err != nil {
		t.Fatalf("save live: %v", err)
	}

	deleted, err := s.PurgeExpiredRefreshTokens(now)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("purged %d, want 1", deleted)
	}
	if _, _, found, err := s.RefreshToken("live"); err != nil || !found {
		t.Fatalf("the live ticket went with the expired one: found=%v err=%v", found, err)
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

// TestIdentityCollectionsAreMigrated: the three collections have to be in the
// index manifest, or a fresh install would run without their uniqueness.
func TestIdentityCollectionsAreMigrated(t *testing.T) {
	manifests, err := migrationsmongo.Load()
	if err != nil {
		t.Fatalf("load manifests: %v", err)
	}
	wanted := map[string]bool{
		"oauth_providers": false, "git_browser_shortcuts": false, "refresh_tokens": false,
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
