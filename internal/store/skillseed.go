package store

import (
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// BundledSkillID returns the stable catalog id for a bundled skill name. The
// id is deterministic so per-type baselines (agent_type_capabilities.ref) and
// per-user acks survive content updates across releases.
func BundledSkillID(name string) string {
	return "skill:bundled-" + name
}

// SeedPlatformSkills upserts the read-only bundled skill catalog from the
// nanobus binary. It is idempotent and safe to run on every boot:
//   - new skills are inserted and enabled on the default type baseline;
//   - a newer version replaces content (files + hash + version), preserving the
//     per-type baseline and per-user acks;
//   - an older version is ignored so a stale binary cannot downgrade content;
//   - a name owned by a non-bundled skill is skipped so it cannot be hijacked.
//
// It returns the number of skills inserted and updated.
func (s *Store) SeedPlatformSkills(skills []nanoprotocol.SkillDef) (inserted, updated int, err error) {
	for _, sk := range skills {
		if sk.Name == "" || len(sk.Files) == 0 {
			continue
		}
		id := BundledSkillID(sk.Name)
		version := sk.Version
		if version <= 0 {
			version = 1
		}

		existing, err := s.GetSkill(id)
		if err == errCapNotFound {
			existing = nil
		} else if err != nil {
			return inserted, updated, fmt.Errorf("seed skill %q: %w", sk.Name, err)
		}

		if existing == nil {
			taken, terr := s.SkillNameTaken(sk.Name, "", id)
			if terr != nil {
				return inserted, updated, fmt.Errorf("seed skill %q: %w", sk.Name, terr)
			}
			if taken {
				// A platform/admin or user skill already owns the name.
				continue
			}
			rec := SkillRecord{
				Capability: Capability{
					ID:             id,
					Scope:          "platform",
					Name:           sk.Name,
					Description:    sk.Description,
					DefaultEnabled: true,
				},
				Files:            sk.Files,
				Origin:           "bundled",
				Status:           "published",
				Version:          version,
				PublishedVersion: version,
				ContentHash:      sk.ContentHash,
				CreatedBy:        "nanobus",
			}
			if cerr := s.CreateSkill(rec); cerr != nil {
				return inserted, updated, fmt.Errorf("seed skill %q: %w", sk.Name, cerr)
			}
			inserted++
			continue
		}

		if existing.Origin != "bundled" {
			continue
		}
		if version < existing.Version {
			continue
		}
		if version == existing.Version && sk.ContentHash == existing.ContentHash {
			continue
		}
		if uerr := s.updateBundledSkillContent(id, sk.Description, sk.Files, version, sk.ContentHash); uerr != nil {
			return inserted, updated, uerr
		}
		updated++
	}
	return inserted, updated, nil
}

// updateBundledSkillContent rewrites only the content fields of a bundled
// skill. The per-type baseline (agent_type_capabilities) and per-user acks are
// deliberately left untouched.
func (s *Store) updateBundledSkillContent(id, description string, files map[string]string, version int, hash string) error {
	ctx, cancel := opCtx()
	defer cancel()

	// The `origin = 'bundled'` condition is the SQL's guard, kept on the write
	// itself so a name that changed hands since the read cannot have its content
	// rewritten.
	res, err := s.db.Collection(skillsCollection).UpdateOne(ctx,
		bson.D{
			{Key: "_id", Value: id},
			{Key: "origin", Value: "bundled"},
		},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "description", Value: description},
			{Key: "files", Value: files},
			{Key: "version", Value: version},
			{Key: "published_version", Value: version},
			{Key: "status", Value: "published"},
			{Key: "content_hash", Value: hash},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}},
	)
	if err != nil {
		return fmt.Errorf("update bundled skill %q: %w", id, err)
	}
	if res.MatchedCount == 0 {
		// Only reachable when the row stopped being a bundled skill between the
		// read above and this write. The SQL matched no row and reported no
		// error, and the seeder counted a skill it had not touched as updated;
		// this is the one place the old code could lie, so it says so instead.
		return fmt.Errorf("update bundled skill %q: no longer a bundled skill", id)
	}
	return nil
}
