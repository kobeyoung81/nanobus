// Package skillbundle holds the canonical, read-only skill packages shipped
// with the nanobus binary. They are seeded into the platform catalog by
// `nanobus skills seed` (run by the bus-init container); agents then
// materialize them from the catalog. Bundled skills cannot be edited or
// deleted through the admin API.
package skillbundle

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

//go:embed skills
var skillsFS embed.FS

//go:embed bundle.json
var bundleJSON []byte

// BundledSkill is one embedded SKILL.md package with its release version.
type BundledSkill struct {
	Name        string
	Description string
	Files       map[string]string // paths relative to the skill dir, incl. SKILL.md
	Version     int
	ContentHash string
}

type manifest struct {
	Skills map[string]skillMeta `json:"skills"`
}

type skillMeta struct {
	Version int `json:"version"`
}

// Load reads every embedded skill package and returns them sorted by name.
func Load() ([]BundledSkill, error) {
	var m manifest
	if err := json.Unmarshal(bundleJSON, &m); err != nil {
		return nil, fmt.Errorf("parse bundle.json: %w", err)
	}

	entries, err := fs.ReadDir(skillsFS, "skills")
	if err != nil {
		return nil, fmt.Errorf("read bundled skills: %w", err)
	}

	out := make([]BundledSkill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		files, err := readSkillDir(name)
		if err != nil {
			return nil, err
		}
		if _, ok := files["SKILL.md"]; !ok {
			return nil, fmt.Errorf("bundled skill %q has no SKILL.md", name)
		}
		version := 1
		if meta, ok := m.Skills[name]; ok && meta.Version > 0 {
			version = meta.Version
		}
		out = append(out, BundledSkill{
			Name:        name,
			Description: frontmatterValue(files["SKILL.md"], "description"),
			Files:       files,
			Version:     version,
			ContentHash: ContentHash(files),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func readSkillDir(name string) (map[string]string, error) {
	root := "skills/" + name
	files := map[string]string{}
	err := fs.WalkDir(skillsFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := skillsFS.ReadFile(path)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(path, root+"/")] = string(data)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read bundled skill %q: %w", name, err)
	}
	return files, nil
}

// ContentHash is the canonical content hash for a skill package: sha256 over
// the sorted relative paths and contents, NUL-separated. Shared with the
// skill save path so stored hashes remain comparable.
func ContentHash(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(files[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// frontmatterValue returns the value of a top-level `key: value` line inside a
// leading `---` YAML block. It is intentionally minimal, matching the runtime
// skill loader; bundled descriptions are single-line.
func frontmatterValue(content, key string) string {
	rest, ok := strings.CutPrefix(content, "---\n")
	if !ok {
		return ""
	}
	block, _, ok := strings.Cut(rest, "\n---")
	if !ok {
		return ""
	}
	for _, line := range strings.Split(block, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
