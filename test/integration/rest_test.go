package integration

// What is left of the integration suite after the storage move: a real store, a
// real server, and the one thing a unit test cannot see — that the two are
// assembled the way main assembles them.
//
// The MySQL suite that used to live beside this one (register a user, post and
// claim messages, and assert the tables exist) had the *storage* as its subject.
// That storage no longer exists, and the same flows are covered harder in
// internal/store against the real store — with documents, concurrency and the
// guards the old suite could not express. Keeping a copy here would only prove
// that MySQL still answers.

import (
	"strings"
	"testing"
	"time"

	"gitee.com/kobeyoung81/nanobus/internal/server"
	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

// TestServerAroundARealStore pins the two things main.go relies on: a server can
// be built around a connected store, and an empty store answers "nothing" rather
// than an error — the first boot reads that as no configuration, and a failure
// there would be indistinguishable from a broken deployment.
func TestServerAroundARealStore(t *testing.T) {
	s := storetest.Connect(t)

	srv := server.NewServer(s)
	if srv == nil {
		t.Fatal("expected a server")
	}

	items, err := s.GetConfigItems("test")
	if err != nil {
		t.Fatalf("an empty store must not be an error: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no configuration, got %d items", len(items))
	}
}

// TestServerAddrIsBoundAfterListen is the assembly check that needs a real
// listener: ListenAndServe binds, and the address it reports is where the
// service can actually be reached.
func TestServerAddrIsBoundAfterListen(t *testing.T) {
	s := storetest.Connect(t)
	srv := server.NewServer(s)

	go func() { _ = srv.ListenAndServe("127.0.0.1:0") }()
	defer func() { _ = srv.Close() }()

	// The address is only assigned once the listener is up.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if addr := srv.Addr(); strings.HasPrefix(addr, "127.0.0.1:") && !strings.HasSuffix(addr, ":0") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the server never reported a bound address (got %q)", srv.Addr())
}
