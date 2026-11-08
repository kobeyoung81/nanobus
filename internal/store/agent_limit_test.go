package store

import "testing"

// The per-tier ceiling on how many agents one account may own. It is enforced in
// CreateAgent, which needs MongoDB; the numbers themselves are asserted here so a
// change to them is a deliberate one rather than a side effect.
func TestMaxAgentsForTier(t *testing.T) {
	cases := []struct {
		tier    string
		limit   int
		limited bool
	}{
		{"free", 5, true},
		{"pro", 10, true},
		{"ultra", 40, true},
		// An unknown tier has no ceiling: that is what the inline map did, and a
		// newly added tier must not silently become unable to create agents.
		{"", 0, false},
		{"enterprise", 0, false},
	}

	for _, c := range cases {
		limit, limited := maxAgentsForTier(c.tier)
		if limit != c.limit || limited != c.limited {
			t.Errorf("maxAgentsForTier(%q) = (%d, %t), want (%d, %t)", c.tier, limit, limited, c.limit, c.limited)
		}
	}
}
