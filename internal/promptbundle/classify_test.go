package promptbundle

import "testing"

// TestClassify pins the five answers the admin page shows. Each case is a state
// the seeder already distinguishes, so a change here without a matching change
// there would make the page disagree with the next deploy.
func TestClassify(t *testing.T) {
	bundled := BundledPrompt{ID: "role_explore", Version: 2, Text: "# Subagent\n\nread only"}
	other := BundledPrompt{ID: "role_explore", Version: 3, Text: "# Subagent\n\nread only, newer"}

	cases := []struct {
		name  string
		text  string
		hash  string
		entry BundledPrompt
		has   bool
		want  Status
	}{
		{"no bundled prompt", "anything", "", other, false, StatusNone},
		{"nothing stored", "", "", bundled, true, StatusEmpty},
		{"the bundled text", bundled.Text, ContentHash(bundled.Text), bundled, true, StatusBundled},
		{"an earlier seed", bundled.Text, ContentHash(bundled.Text), other, true, StatusStale},
		{"an admin edit", "my own prompt", ContentHash(bundled.Text), bundled, true, StatusEdited},
		{"no baseline at all", "pre-bundle text", "", bundled, true, StatusEdited},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.text, c.hash, c.entry, c.has); got != c.want {
				t.Fatalf("Classify() = %q, want %q", got, c.want)
			}
		})
	}
}
