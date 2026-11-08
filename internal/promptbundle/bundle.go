// Package promptbundle holds the canonical system prompts for the built-in
// agent types. They ship with the nanobus binary and are seeded into
// agent_types by `nanobus prompts seed` (run by the bus-init container on every
// deploy).
//
// Prompt text lives in the database because it is admin-editable on the Agent
// Types page; the bundle is therefore only the *platform baseline*. The seeder
// replaces a prompt only when the stored text still matches the baseline hash it
// recorded on the previous run, so an admin edit is never silently clobbered.
// See store.SeedPlatformPrompts and docs/BUNDLED_PROMPTS_DESIGN.md.
package promptbundle

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// The `all:` prefix matters: go:embed skips files whose name starts with `_` by
// default, and `prompts/_common.md` — the shared head prompt of the session
// agents — is exactly that.
//
//go:embed all:prompts
var promptsFS embed.FS

//go:embed bundle.json
var bundleJSON []byte

// BundledPrompt is one embedded system prompt, keyed by agent_types.id.
type BundledPrompt struct {
	ID      string
	Version int
	Text    string
	// ContentHash is sha256(Text); it is recorded in the type's settings so the
	// next run can tell an unmodified seed from an admin edit.
	ContentHash string
}

type manifest struct {
	Prompts map[string]promptMeta `json:"prompts"`
}

type promptMeta struct {
	Version int `json:"version"`
}

// Load reads every embedded prompt, sorted by agent-type id. Each prompt must
// be listed in bundle.json; an unlisted file is an error rather than a silent
// version-0 entry.
func Load() ([]BundledPrompt, error) {
	var m manifest
	if err := json.Unmarshal(bundleJSON, &m); err != nil {
		return nil, fmt.Errorf("parse bundle.json: %w", err)
	}

	entries, err := fs.ReadDir(promptsFS, "prompts")
	if err != nil {
		return nil, fmt.Errorf("read bundled prompts: %w", err)
	}

	out := make([]BundledPrompt, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		// `_`-prefixed files are not agent types: they are shared material
		// (currently `_common.md`, the session agents' head prompt) read through
		// their own accessor.
		if strings.HasPrefix(e.Name(), "_") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		meta, ok := m.Prompts[id]
		if !ok {
			return nil, fmt.Errorf("bundled prompt %q is not listed in bundle.json", id)
		}
		data, err := promptsFS.ReadFile("prompts/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read bundled prompt %q: %w", id, err)
		}
		// The prompt is stored and rendered verbatim, so a trailing newline
		// would show up as a blank line in the Agent Types preview. Normalise
		// before hashing so the recorded baseline matches the seeded text.
		text := strings.TrimRight(string(data), "\r\n")
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("bundled prompt %q is empty", id)
		}
		out = append(out, BundledPrompt{
			ID:          id,
			Version:     meta.Version,
			Text:        text,
			ContentHash: ContentHash(text),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// LoadSharedHead reads the shared head prompt of the session agents
// (`prompts/_common.md`). Build and Plan receive this text instead of their own
// type prompt, which is what keeps the request prefix identical across a mode
// switch; the type prompt becomes the per-turn mode note. An absent file is not
// an error: the platform row is simply left unseeded, and the runtime keeps its
// built-in default.
func LoadSharedHead() (BundledPrompt, error) {
	data, err := promptsFS.ReadFile("prompts/_common.md")
	if err != nil {
		return BundledPrompt{}, nil
	}
	text := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(text) == "" {
		return BundledPrompt{}, fmt.Errorf("bundled shared head prompt is empty")
	}
	var m manifest
	if err := json.Unmarshal(bundleJSON, &m); err != nil {
		return BundledPrompt{}, fmt.Errorf("parse bundle.json: %w", err)
	}
	version := 0
	if meta, ok := m.Prompts["_common"]; ok {
		version = meta.Version
	}
	return BundledPrompt{ID: "_common", Version: version, Text: text, ContentHash: ContentHash(text)}, nil
}

// ContentHash is the canonical hash of a prompt: sha256 over the text, hex.
// It deliberately matches MySQL's SHA2(text, 256), so a baseline stamped by SQL
// (migration 070) compares equal to one computed here.
func ContentHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
