// Package migrationsmongo holds the declarative MongoDB migrations.
//
// They differ in kind from the SQL migrations they replaced: a SQL migration ran
// statements, while one of these *describes the shape a set of collections must
// have* (its indexes, and optionally a validator). Creating an index that
// already exists is a no-op in MongoDB, so a manifest that is applied twice, or
// applied after a manual index was added, converges instead of failing.
//
// There is no SQL set next door any more — it was deleted once the last domain
// moved — so these manifests are the only description of the store's shape.
package migrationsmongo

import "embed"

//go:embed *.json
var FS embed.FS

// Load returns all migration manifests as a name→content map.
func Load() (map[string]string, error) {
	entries, err := FS.ReadDir(".")
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := FS.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		m[e.Name()] = string(data)
	}
	return m, nil
}
