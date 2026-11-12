package store_test

// Onboarding a channel identity: the account and the container request are not
// enough — somebody has to answer. These tests pin the three outcomes: the default
// agent is created and travels with the container request, a failure is not fatal,
// and an identity that already exists is left alone.

import (
	"context"
	"errors"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

type enqueueCall struct {
	userID    string
	bindingID int64
	agentID   string
	level     string
}

type fakeEnqueuer struct{ calls []enqueueCall }

func (f *fakeEnqueuer) Enabled() bool { return true }

func (f *fakeEnqueuer) Enqueue(_ context.Context, userID string, bindingID int64, agentID, level string) error {
	f.calls = append(f.calls, enqueueCall{userID, bindingID, agentID, level})
	return nil
}

type fakeEnsurer struct {
	id    string
	err   error
	calls []string
}

func (f *fakeEnsurer) Enabled() bool { return true }

func (f *fakeEnsurer) EnsureDefaultAgent(_ context.Context, userID string) (string, error) {
	f.calls = append(f.calls, userID)
	return f.id, f.err
}

func TestOnboardingCreatesAnAgentAndPassesItToTheQueue(t *testing.T) {
	s := &store.Store{}
	s.SetAccountBackend(storetest.NewFakeAccounts())
	ensurer := &fakeEnsurer{id: "u-new-1-default"}
	enqueuer := &fakeEnqueuer{}
	s.SetAgentEnsurer(ensurer)
	s.SetContainerEnqueuer(enqueuer)

	resolved, err := s.ResolveUserBinding("web", "tab-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !resolved.IsNew {
		t.Fatal("the first resolve did not report a new account")
	}
	if len(ensurer.calls) != 1 || ensurer.calls[0] != resolved.UserID {
		t.Fatalf("ensure calls = %v, want one for %s", ensurer.calls, resolved.UserID)
	}
	if len(enqueuer.calls) != 1 {
		t.Fatalf("enqueue calls = %v, want one", enqueuer.calls)
	}
	if got := enqueuer.calls[0].agentID; got != "u-new-1-default" {
		t.Fatalf("the container request names agent %q, want the default agent", got)
	}
	if enqueuer.calls[0].userID != resolved.UserID || enqueuer.calls[0].level != "free" {
		t.Fatalf("enqueue call = %+v", enqueuer.calls[0])
	}
}

// A portal failure must not fail the onboarding: the account and the binding are
// still useful, and the console can create the agent by hand.
func TestOnboardingSurvivesAnAgentFailure(t *testing.T) {
	s := &store.Store{}
	s.SetAccountBackend(storetest.NewFakeAccounts())
	s.SetAgentEnsurer(&fakeEnsurer{err: errors.New("portal is down")})
	enqueuer := &fakeEnqueuer{}
	s.SetContainerEnqueuer(enqueuer)

	resolved, err := s.ResolveUserBinding("web", "tab-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(enqueuer.calls) != 1 {
		t.Fatalf("enqueue calls = %v, want one despite the agent failure", enqueuer.calls)
	}
	// The legacy per-binding placeholder is what the queue got before this hook.
	if got, want := enqueuer.calls[0].agentID, "binding-1"; got != want {
		t.Fatalf("agent id = %q, want the binding placeholder %q", got, want)
	}
	if resolved.UserID == "" {
		t.Fatal("the resolve returned no user")
	}
}

// An identity that already has a binding is not touched: no ensure call, no new
// container request.
func TestExistingIdentityDoesNotEnsureAnAgent(t *testing.T) {
	accounts := storetest.NewFakeAccounts()
	accounts.Seed("u-1", "pro", "wecom", "sender-1", 7)
	s := &store.Store{}
	s.SetAccountBackend(accounts)
	ensurer := &fakeEnsurer{id: "never"}
	s.SetAgentEnsurer(ensurer)
	enqueuer := &fakeEnqueuer{}
	s.SetContainerEnqueuer(enqueuer)

	resolved, err := s.ResolveUserBinding("wecom", "sender-1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.IsNew || resolved.UserID != "u-1" || resolved.BindingID != 7 {
		t.Fatalf("resolved = %+v", resolved)
	}
	if len(ensurer.calls) != 0 {
		t.Fatalf("ensure was called for an existing identity: %v", ensurer.calls)
	}
	if len(enqueuer.calls) != 0 {
		t.Fatalf("a container was requested for an existing identity: %v", enqueuer.calls)
	}
}
