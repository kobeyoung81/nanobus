package promptbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestLoad_ReturnsEveryListedPrompt guards the bundle against a prompt file that
// exists but was never given a version, or a manifest entry with no file.
func TestLoad_ReturnsEveryListedPrompt(t *testing.T) {
	prompts, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(prompts) == 0 {
		t.Fatal("bundle is empty")
	}
	want := map[string]bool{
		"build": true, "plan": true,
		"role_general": true, "role_explore": true, "role_scout": true,
		"role_summarize": true, "role_title": true,
		"utility_memory_flush": true,
	}
	if len(prompts) != len(want) {
		t.Errorf("loaded %d prompts, want %d", len(prompts), len(want))
	}
	for _, p := range prompts {
		if !want[p.ID] {
			t.Errorf("unexpected bundled prompt %q", p.ID)
		}
		delete(want, p.ID)
		if p.Version <= 0 {
			t.Errorf("%s has no version", p.ID)
		}
		if p.ContentHash != ContentHash(p.Text) {
			t.Errorf("%s content hash does not match its text", p.ID)
		}
		// A trailing newline in the source file would be seeded verbatim and
		// render as a blank line in the Agent Types markdown preview.
		if strings.HasSuffix(p.Text, "\n") {
			t.Errorf("%s text ends with a newline", p.ID)
		}
		// The seeder rounds-trips the text through the DB column (TEXT, 64 KiB).
		if len(p.Text) > 60*1024 {
			t.Errorf("%s is %d bytes; keep bundled prompts well under the 64 KiB TEXT class", p.ID, len(p.Text))
		}
	}
	for id := range want {
		t.Errorf("listed prompt %q has no file", id)
	}
}

// TestLoad_SortedByID keeps the seed output stable for operators.
func TestLoad_SortedByID(t *testing.T) {
	prompts, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for i := 1; i < len(prompts); i++ {
		if prompts[i-1].ID >= prompts[i].ID {
			t.Fatalf("bundle not sorted: %q before %q", prompts[i-1].ID, prompts[i].ID)
		}
	}
}

// TestContentHash_MatchesSHA256 pins the hash to the algorithm MySQL's
// SHA2(x, 256) uses, because migration 070 stamps the baseline with it.
func TestContentHash_MatchesSHA256(t *testing.T) {
	sum := sha256.Sum256([]byte("hello"))
	if got, want := ContentHash("hello"), hex.EncodeToString(sum[:]); got != want {
		t.Errorf("ContentHash = %q, want %q", got, want)
	}
}

// TestBuildAndPlanPrompts_CarryTheirContract spot-checks the two session-agent
// prompts for the guidance the rework exists to add.
func TestBuildAndPlanPrompts_CarryTheirContract(t *testing.T) {
	prompts, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byID := map[string]string{}
	for _, p := range prompts {
		byID[p.ID] = p.Text
	}
	// Build and Plan now share one head prompt (`_common.md`), which the bus folds
	// into both session definitions; each type's own prompt is the short mode note
	// appended to the tail of every request. So the contract is checked over what the
	// model actually receives: the shared head plus that mode's note.
	head, err := LoadSharedHead()
	if err != nil {
		t.Fatalf("load shared head: %v", err)
	}
	if head.Text == "" {
		t.Fatal("the shared head prompt must ship with the bundle")
	}
	// The prompts are deliberately toolchain-neutral: they name no language's
	// commands — the plan's *how to verify* carries the specifics — and the
	// "save a long answer to markdown/PDF" rule was dropped as obsolete.
	build := head.Text + "\n\n" + byID["build"]
	plan := head.Text + "\n\n" + byID["plan"]
	for _, want := range []string{"plan_execute", "todowrite", "question", "toolchain", "the user's language", "SKILL.md", "sharedspace"} {
		if !strings.Contains(build, want) {
			t.Errorf("build prompt is missing %q", want)
		}
	}
	for _, want := range []string{"plan_write", "question", "AGENT.md", "deliberately not doing", "the user's language", "SKILL.md", "sharedspace"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan prompt is missing %q", want)
		}
	}
	for _, prompt := range []string{build, plan} {
		if strings.Contains(prompt, "go build") {
			t.Error("a session prompt names a language-specific command")
		}
		if strings.Contains(prompt, "PDF") {
			t.Error("the removed long-answer-to-file rule is back")
		}
	}
}

// TestBundledPrompts_StayToolchainNeutralAndKeepCodeContracts guards two classes
// of accidental regression across every bundled prompt: naming one language's
// commands or manifests, and dropping a literal the runtime matches exactly.
func TestBundledPrompts_StayToolchainNeutralAndKeepCodeContracts(t *testing.T) {
	prompts, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	byID := map[string]string{}
	for _, p := range prompts {
		byID[p.ID] = p.Text
		for _, banned := range []string{"go build", "gofmt", "go test", "npm run", "package.json", "go.mod"} {
			if strings.Contains(p.Text, banned) {
				t.Errorf("%s names a language-specific command or manifest (%q)", p.ID, banned)
			}
		}
	}
	if !strings.Contains(byID["utility_memory_flush"], "NOTHING_TO_SAVE") {
		t.Error("utility_memory_flush lost its NOTHING_TO_SAVE sentinel, which the runtime matches exactly")
	}
}

// TestLoadSharedHead_CarriesTheChunkedWriteRule pins both halves of a shared-head
// change: the rule itself, and the manifest version that records it. The seeder
// stamps `prompt_version` from bundle.json onto the platform row, so a text
// change without a bump makes the admin page report the old baseline version.
func TestLoadSharedHead_CarriesTheChunkedWriteRule(t *testing.T) {
	head, err := LoadSharedHead()
	if err != nil {
		t.Fatalf("load shared head: %v", err)
	}
	for _, want := range []string{"append: true", "16 KiB"} {
		if !strings.Contains(head.Text, want) {
			t.Errorf("shared head is missing the chunked-write rule (%q)", want)
		}
	}
	if head.Version != 3 {
		t.Errorf("shared head version = %d, want 3 (bump bundle.json when _common.md changes)", head.Version)
	}
}
