package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// The account administration endpoints. They exist because the portal used to
// read `users` out of MySQL — the same table the bus reads — and what has to hold
// now is that the console can still do everything it did, through the bus.

func accountRequest(t *testing.T, s *Server, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, req)
	return rec
}

func setRole(t *testing.T, s *Server, userID, role string) {
	t.Helper()
	if _, err := s.store.DB().Collection("users").UpdateOne(context.Background(),
		bson.D{{Key: "_id", Value: userID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "role", Value: role}}}},
	); err != nil {
		t.Fatalf("set role: %v", err)
	}
}

func TestRestListUserAccounts(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUserChannel(t, srv.store, "u-1", "pro", "telegram", "sender-1", 1)
	setRole(t, srv, "u-1", "admin")
	storetest.SeedUserChannel(t, srv.store, "u-2", "free", "web", "sender-2", 2)
	if err := srv.store.UpdateUserStatus("u-2", "deleted"); err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}

	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/user-accounts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Users []struct {
			UserID      string   `json:"user_id"`
			DisplayName string   `json:"display_name"`
			Status      string   `json:"status"`
			Tier        string   `json:"tier"`
			Role        string   `json:"role"`
			Channels    []string `json:"channels"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Users) != 1 {
		t.Fatalf("expected only the live account, got %+v", body.Users)
	}
	got := body.Users[0]
	if got.UserID != "u-1" || got.Status != "active" || got.Tier != "pro" || got.Role != "admin" {
		t.Fatalf("unexpected account: %+v", got)
	}
	// The console shows which channels an account is reachable on.
	if len(got.Channels) != 1 || got.Channels[0] != "telegram" {
		t.Fatalf("channels missing: %+v", got.Channels)
	}
	// And never the credential.
	if body, _ := json.Marshal(got); bytes.Contains(body, []byte("password")) {
		t.Fatalf("the listing leaked a credential field: %s", body)
	}

	all := accountRequest(t, srv, http.MethodGet, "/api/v1/user-accounts?include_deleted=1", nil)
	var withDeleted struct {
		Users []struct {
			UserID string `json:"user_id"`
		} `json:"users"`
	}
	if err := json.Unmarshal(all.Body.Bytes(), &withDeleted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(withDeleted.Users) != 2 {
		t.Fatalf("expected both accounts with include_deleted, got %+v", withDeleted.Users)
	}
}

func TestRestCountUserAccounts(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUser(t, srv.store, "u-1", "pro", 1)
	storetest.SeedUser(t, srv.store, "u-2", "free", 2)
	setRole(t, srv, "u-2", "admin")
	if err := srv.store.UpdateUserStatus("u-2", "deleted"); err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}

	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/user-accounts/counts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Active int            `json:"active"`
		Admins int            `json:"admins"`
		Tiers  map[string]int `json:"tiers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The deleted admin counts as neither.
	if body.Active != 1 || body.Admins != 0 {
		t.Fatalf("counts = %+v, want active 1 admins 0", body)
	}
	// The tier breakdown carries all three columns, zero included: an absent key
	// would read as "not counted" rather than "none".
	if len(body.Tiers) != 3 || body.Tiers["pro"] != 1 || body.Tiers["free"] != 1 || body.Tiers["ultra"] != 0 {
		t.Fatalf("unexpected tiers: %+v", body.Tiers)
	}
}

func TestRestGetUserAccount(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUserChannel(t, srv.store, "u-1", "pro", "telegram", "sender-1", 1)

	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/user-accounts/u-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		UserID   string   `json:"user_id"`
		Status   string   `json:"status"`
		Tier     string   `json:"tier"`
		Channels []string `json:"channels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UserID != "u-1" || body.Status != "active" || body.Tier != "pro" {
		t.Fatalf("unexpected account: %+v", body)
	}
	if len(body.Channels) != 1 || body.Channels[0] != "telegram" {
		t.Fatalf("channels missing: %+v", body.Channels)
	}

	missing := accountRequest(t, srv, http.MethodGet, "/api/v1/user-accounts/ghost", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", missing.Code)
	}
}

func TestRestSetUserAccount(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUser(t, srv.store, "u-1", "free", 1)

	roles := "admin"
	rec := accountRequest(t, srv, http.MethodPatch, "/api/v1/user-accounts/u-1", map[string]any{"role": roles})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// Only the named field changed: status and tier are untouched.
	status, tier, err := srv.store.GetUserInfo("u-1")
	if err != nil {
		t.Fatalf("GetUserInfo: %v", err)
	}
	if status != "active" || tier != "free" {
		t.Fatalf("a partial update touched other fields: status %q tier %q", status, tier)
	}
	users, err := srv.store.ListUserSummaries(false, 0)
	if err != nil {
		t.Fatalf("ListUserSummaries: %v", err)
	}
	if len(users) != 1 || users[0].Role != "admin" {
		t.Fatalf("role not updated: %+v", users)
	}

	// A partial update carries what it names, in one call.
	tier2 := "ultra"
	status2 := "suspended"
	if rec := accountRequest(t, srv, http.MethodPatch, "/api/v1/user-accounts/u-1",
		map[string]any{"tier": tier2, "status": status2}); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if status, tier, err = srv.store.GetUserInfo("u-1"); err != nil || status != "suspended" || tier != "ultra" {
		t.Fatalf("expected suspended/ultra, got %q/%q (err %v)", status, tier, err)
	}

	// An empty update is refused rather than silently accepted.
	if rec := accountRequest(t, srv, http.MethodPatch, "/api/v1/user-accounts/u-1", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	// So is an unknown account.
	if rec := accountRequest(t, srv, http.MethodPatch, "/api/v1/user-accounts/ghost",
		map[string]any{"role": "admin"}); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestRestUserPassword covers the two credential endpoints: the portal stores a
// hash it produced, and reads it back to verify a login.
func TestRestUserPassword(t *testing.T) {
	srv := testServerMongo(t)
	storetest.SeedUser(t, srv.store, "u-1", "free", 1)

	// An account with no password reads as having none, not as missing.
	rec := accountRequest(t, srv, http.MethodGet, "/api/v1/user-accounts/u-1/password-hash", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var hashBody struct {
		PasswordHash string `json:"password_hash"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &hashBody); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if hashBody.PasswordHash != "" {
		t.Fatalf("expected no hash, got %q", hashBody.PasswordHash)
	}
	// But an unknown account is a 404, which is how the login path tells the two
	// apart.
	if rec := accountRequest(t, srv, http.MethodGet, "/api/v1/user-accounts/ghost/password-hash", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}

	rec = accountRequest(t, srv, http.MethodPut, "/api/v1/user-accounts/u-1/password", map[string]any{
		"password_hash":        "bcrypt-hash",
		"must_change_password": true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	hash, err := srv.store.UserPasswordHash("u-1")
	if err != nil {
		t.Fatalf("UserPasswordHash: %v", err)
	}
	if hash != "bcrypt-hash" {
		t.Fatalf("hash = %q", hash)
	}
	// The forced-change flag travelled with it.
	var doc struct {
		MustChange bool `bson:"must_change_password"`
	}
	if err := srv.store.DB().Collection("users").
		FindOne(context.Background(), bson.D{{Key: "_id", Value: "u-1"}}).Decode(&doc); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if !doc.MustChange {
		t.Fatal("must_change_password was not stored")
	}

	// A body without a hash is refused: storing "no password" is what an empty
	// PUT would mean, and it is not something the console can ask for.
	if rec := accountRequest(t, srv, http.MethodPut, "/api/v1/user-accounts/u-1/password",
		map[string]any{"must_change_password": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The member picker in the library searches by fragment, not by exact address.
// What has to hold: a name fragment finds the person, an address fragment finds
// them too, nothing found is an empty list rather than an error, and a deleted
// account never shows up as an invitation candidate.
func TestRestSearchUserAccounts(t *testing.T) {
	srv := testServerMongo(t)

	seed := func(id, name, email string, binding int64) {
		t.Helper()
		storetest.SeedUser(t, srv.store, id, "free", binding)
		if _, err := srv.store.DB().Collection("users").UpdateOne(context.Background(),
			bson.D{{Key: "_id", Value: id}},
			bson.D{{Key: "$set", Value: bson.D{
				{Key: "display_name", Value: name},
				{Key: "email", Value: email},
			}}}); err != nil {
			t.Fatalf("seed identity %s: %v", id, err)
		}
	}
	seed("u-1", "林岚", "linlan@example.com", 1)
	seed("u-2", "周野", "zhouye@example.com", 2)

	search := func(q string) []struct {
		UserID      string `json:"user_id"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	} {
		t.Helper()
		rec := accountRequest(t, srv, http.MethodGet, "/api/v1/user-accounts?q="+url.QueryEscape(q), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("q=%q status = %d: %s", q, rec.Code, rec.Body.String())
		}
		var body struct {
			Users []struct {
				UserID      string `json:"user_id"`
				DisplayName string `json:"display_name"`
				Email       string `json:"email"`
			} `json:"users"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body.Users
	}

	if got := search("林"); len(got) != 1 || got[0].UserID != "u-1" {
		t.Fatalf("name fragment matched %+v, want u-1 only", got)
	}
	if got := search("zhouye@"); len(got) != 1 || got[0].UserID != "u-2" {
		t.Fatalf("address fragment matched %+v, want u-2 only", got)
	}
	if got := search("nobody-would-match"); len(got) != 0 {
		t.Fatalf("expected an empty list, got %+v", got)
	}

	if err := srv.store.UpdateUserStatus("u-1", "deleted"); err != nil {
		t.Fatalf("UpdateUserStatus: %v", err)
	}
	if got := search("林"); len(got) != 0 {
		t.Fatalf("a deleted account was offered as a candidate: %+v", got)
	}
}
