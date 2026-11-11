package storetest

// A fake account backend for the bus's own tests. The real one is the auth service
// over HTTP; what the bus's tests need is a place to keep accounts that behaves the
// way the handlers assume (ids, one administrator hand-out, channel links).

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// FakeAccounts is an in-memory store.AccountBackend.
type FakeAccounts struct {
	mu     sync.Mutex
	users  map[string]*store.UserSummary
	links  map[string][]string // user id → channel names
	pw     map[string]string   // user id → hash
	flags  map[string]bool     // "must_change"
	bound  map[string]string   // user id → binding identity
	bindID map[string]int64    // user id → binding id
	claim  string              // the account that took the admin hand-out
	next   int64
}

// NewFakeAccounts returns an empty backend.
func NewFakeAccounts() *FakeAccounts {
	return &FakeAccounts{
		users: map[string]*store.UserSummary{}, links: map[string][]string{},
		pw: map[string]string{}, flags: map[string]bool{},
		bound: map[string]string{}, bindID: map[string]int64{},
	}
}

// Seed writes an account the way onboarding or the console would: status active, the
// given tier, and (when channel is set) one linked identity.
func (f *FakeAccounts) Seed(userID, tier, channel, senderID string, bindingID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.user(userID)
	u.Tier = tier
	u.Status = "active"
	if channel != "" {
		f.links[userID] = append(f.links[userID], channel)
		f.bound[userID] = channel + "/" + senderID
		f.bindID[userID] = bindingID
	}
}

func (f *FakeAccounts) user(userID string) *store.UserSummary {
	if u, ok := f.users[userID]; ok {
		return u
	}
	u := &store.UserSummary{ID: userID, Role: "user", Status: "active", Tier: "free", UpdatedAt: time.Now().UTC()}
	f.users[userID] = u
	return u
}

func (f *FakeAccounts) Resolve(_ context.Context, channel, senderID string) (store.ResolvedBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := channel + "/" + senderID
	for id, ident := range f.bound {
		if ident != key {
			continue
		}
		for _, name := range f.links[id] {
			if name == channel {
				return store.ResolvedBinding{UserID: id, BindingID: f.bindID[id]}, nil
			}
		}
		return store.ResolvedBinding{}, store.ErrChannelDisabled
	}
	f.next++
	id := "u-new-" + itoa64(f.next)
	f.user(id)
	f.links[id] = []string{channel}
	f.bound[id] = key
	f.bindID[id] = f.next
	u := f.users[id]
	u.Status = "pending"
	return store.ResolvedBinding{UserID: id, BindingID: f.next, IsNew: true}, nil
}

func (f *FakeAccounts) Register(_ context.Context, userID, displayName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.user(userID)
	if displayName != "" {
		u.DisplayName = displayName
	}
	return nil
}

func (f *FakeAccounts) LinkChannel(_ context.Context, userID, channel, senderID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.user(userID)
	key := channel + "/" + senderID
	for id, ident := range f.bound {
		if ident == key && id != userID {
			delete(f.bound, id)
			f.links[id] = nil
			f.bindID[userID] = f.bindID[id]
		}
	}
	f.bound[userID] = key
	f.links[userID] = append(f.links[userID], channel)
	return nil
}

func (f *FakeAccounts) Account(_ context.Context, userID string) (*store.UserSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return nil, nil
	}
	out := *u
	return &out, nil
}

func (f *FakeAccounts) AccountByEmail(_ context.Context, email string) (*store.UserSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.Email == email {
			out := *u
			return &out, nil
		}
	}
	return nil, nil
}

func (f *FakeAccounts) ListAccounts(_ context.Context, includeDeleted bool, limit int) ([]store.UserSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.users))
	for id := range f.users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := []store.UserSummary{}
	for _, id := range ids {
		u := f.users[id]
		if !includeDeleted && u.Status == "deleted" {
			continue
		}
		out = append(out, *u)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *FakeAccounts) SearchAccounts(_ context.Context, q string, limit int) ([]store.UserSummary, error) {
	// Deleted accounts are not invitation candidates, which is the module's own
	// rule (the search excludes them; the list page can include them).
	all, _ := f.ListAccounts(context.Background(), false, 0)
	out := []store.UserSummary{}
	for _, u := range all {
		if strings.Contains(strings.ToLower(u.ID), strings.ToLower(q)) ||
			strings.Contains(strings.ToLower(u.DisplayName), strings.ToLower(q)) ||
			strings.Contains(strings.ToLower(u.Email), strings.ToLower(q)) {
			out = append(out, u)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *FakeAccounts) AllUserIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.users))
	for id := range f.users {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (f *FakeAccounts) Patch(_ context.Context, userID string, p store.AccountPatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return store.ErrUserNotFound
	}
	if p.Status != nil {
		u.Status = *p.Status
	}
	if p.Role != nil {
		u.Role = *p.Role
	}
	if p.Tier != nil {
		u.Tier = *p.Tier
	}
	if p.DisplayName != nil {
		u.DisplayName = *p.DisplayName
	}
	if p.Email != nil {
		for id, other := range f.users {
			if id != userID && other.Email == *p.Email && *p.Email != "" {
				return store.ErrEmailTaken
			}
		}
		u.Email = *p.Email
	}
	if p.PreferredLanguage != nil {
		u.PreferredLanguage = *p.PreferredLanguage
	}
	u.UpdatedAt = time.Now().UTC()
	return nil
}

func (f *FakeAccounts) PasswordHash(_ context.Context, userID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pw[userID], nil
}

func (f *FakeAccounts) SetPassword(_ context.Context, userID, hash string, mustChange, keepFlag bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return store.ErrUserNotFound
	}
	f.pw[userID] = hash
	if !keepFlag {
		f.flags[userID] = mustChange
	}
	u.MustChangePassword = f.flags[userID]
	return nil
}

func (f *FakeAccounts) ClaimBootstrapAdmin(_ context.Context, userID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claim != "" {
		return false, nil
	}
	f.claim = userID
	return true, nil
}

func (f *FakeAccounts) ReleaseBootstrapAdmin(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claim == userID {
		f.claim = ""
	}
	return nil
}

func (f *FakeAccounts) Channels(_ context.Context, userID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[userID]; !ok {
		return nil, store.ErrUserNotFound
	}
	return append([]string{}, f.links[userID]...), nil
}

func (f *FakeAccounts) SetChannel(_ context.Context, userID, channel string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[userID]; !ok {
		return store.ErrUserNotFound
	}
	kept := make([]string, 0, len(f.links[userID]))
	for _, name := range f.links[userID] {
		if name != channel {
			kept = append(kept, name)
		}
	}
	if enabled {
		kept = append(kept, channel)
	}
	f.links[userID] = kept
	return nil
}

func (f *FakeAccounts) Info(_ context.Context, userID string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return "", "", store.ErrUserNotFound
	}
	return u.Status, u.Tier, nil
}

func (f *FakeAccounts) Counts(context.Context) (int, int, map[string]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	active, admins := 0, 0
	tiers := map[string]int{"free": 0, "pro": 0, "ultra": 0}
	for _, u := range f.users {
		if u.Status == "active" {
			active++
			if u.Role == "admin" {
				admins++
			}
		}
		tiers[u.Tier]++
	}
	return active, admins, tiers, nil
}

func (f *FakeAccounts) Exists(_ context.Context, userID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.users[userID]
	return ok, nil
}

func (f *FakeAccounts) BindingOwner(_ context.Context, bindingID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, got := range f.bindID {
		if got == bindingID {
			return id, nil
		}
	}
	return "", store.ErrUserNotFound
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }
