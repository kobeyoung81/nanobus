package store_test

// The bootstrap administrator is the one role decision the system makes on its
// own: the first account created through a sign-up takes it while the
// installation has nobody in the role. What matters is that exactly one account
// can win — counting administrators is not enough when two registrations arrive
// together — and that the claim is one-shot.

import (
	"sync"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store/storetest"
)

func TestClaimBootstrapAdmin_IsOneShot(t *testing.T) {
	s := storetest.Connect(t)

	first, err := s.ClaimBootstrapAdmin("u-1")
	if err != nil || !first {
		t.Fatalf("first claim = %v, %v; want true, nil", first, err)
	}
	again, err := s.ClaimBootstrapAdmin("u-2")
	if err != nil || again {
		t.Fatalf("second claim = %v, %v; want false, nil", again, err)
	}
}

func TestClaimBootstrapAdmin_ConcurrentClaimHasOneWinner(t *testing.T) {
	s := storetest.Connect(t)

	const claimants = 8
	var wg sync.WaitGroup
	wins := make([]bool, claimants)
	for i := 0; i < claimants; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			won, err := s.ClaimBootstrapAdmin("u-" + string(rune('a'+i)))
			if err != nil {
				t.Errorf("claim %d: %v", i, err)
			}
			wins[i] = won
		}(i)
	}
	wg.Wait()

	winners := 0
	for _, won := range wins {
		if won {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly one winning claim, got %d (%v)", winners, wins)
	}
}

func TestReleaseBootstrapAdmin_AllowsAnotherClaim(t *testing.T) {
	s := storetest.Connect(t)

	if won, err := s.ClaimBootstrapAdmin("u-1"); err != nil || !won {
		t.Fatalf("first claim = %v, %v", won, err)
	}
	if err := s.ReleaseBootstrapAdmin("u-1"); err != nil {
		t.Fatalf("ReleaseBootstrapAdmin: %v", err)
	}
	if won, err := s.ClaimBootstrapAdmin("u-2"); err != nil || !won {
		t.Fatalf("claim after release = %v, %v; want true, nil", won, err)
	}
	// Releasing a claim another account owns must not free it.
	if err := s.ReleaseBootstrapAdmin("u-1"); err != nil {
		t.Fatalf("ReleaseBootstrapAdmin(foreign): %v", err)
	}
	if won, err := s.ClaimBootstrapAdmin("u-3"); err != nil || won {
		t.Fatalf("claim after foreign release = %v, %v; want false, nil", won, err)
	}
}
