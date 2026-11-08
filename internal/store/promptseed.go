package store

import (
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanobus/internal/promptbundle"
)

// SeedPlatformPrompts writes the bundled system prompts onto the seeded agent
// types. It is idempotent and safe to run on every boot (the bus-init container
// does exactly that), and it never touches anything but `prompt` and the two
// baseline fields:
//
//   - a type that does not exist is skipped — the bundle never creates types;
//   - a prompt with no recorded baseline is treated as admin-owned and only
//     filled when it is empty;
//   - a prompt whose text still hashes to the recorded baseline is an unmodified
//     platform seed, so it is replaced with the bundled text;
//   - a prompt edited after seeding is left alone (counted as skipped) unless
//     force is set, so an admin edit is never silently clobbered;
//   - a prompt already equal to the bundled text is a no-op.
//
// Migration 070 stamps the baseline for prompt text that predates the bundle, so
// the first run after that migration replaces it; every later run is a no-op
// until the bundled text changes.
//
// The baseline is two fields on the type document — `prompt_hash` and
// `prompt_version` — rather than entries inside `settings`. `settings` holds
// string-encoded runtime knobs, and the version is a number: the SQL column
// carried it as a JSON number inside an object every reader decoded into a
// map[string]string, which is precisely why the whole map was silently dropped
// there. Keeping the marker out of the map also keeps it out of the admin API.
//
// It returns how many prompts were updated and how many were left as-is.
func (s *Store) SeedPlatformPrompts(prompts []promptbundle.BundledPrompt, force bool) (updated, skipped int, err error) {
	ctx, cancel := opCtx()
	defer cancel()
	coll := s.db.Collection(agentTypesCollection)

	for _, p := range prompts {
		if p.ID == "" || p.Text == "" {
			continue
		}
		var doc struct {
			Prompt     string `bson:"prompt"`
			PromptHash string `bson:"prompt_hash"`
		}
		// Projected, because a prompt is the largest field a type has and the
		// seeder runs over every type on every boot.
		rowErr := coll.FindOne(ctx, bson.D{{Key: "_id", Value: p.ID}},
			options.FindOne().SetProjection(bson.D{
				{Key: "prompt", Value: 1},
				{Key: "prompt_hash", Value: 1},
			}),
		).Decode(&doc)

		current := doc.Prompt
		recorded := doc.PromptHash
		switch {
		case errors.Is(rowErr, mongo.ErrNoDocuments):
			skipped++
			continue
		case rowErr != nil:
			return updated, skipped, fmt.Errorf("seed prompt %q: %w", p.ID, rowErr)
		case recorded == "" && current != "":
			// No baseline: an admin owns this prompt (or it predates migration
			// 070). Never overwrite it blind.
			skipped++
			continue
		case recorded != "" && recorded != promptbundle.ContentHash(current) && !force:
			// Edited after the last seed: keep the edit.
			skipped++
			continue
		case current == p.Text:
			// Already the bundled text.
			skipped++
			continue
		}

		if err := s.updateTypePrompt(p.ID, p.Text, p.Version); err != nil {
			return updated, skipped, err
		}
		updated++
	}
	return updated, skipped, nil
}

// updateTypePrompt writes the prompt and restamps its baseline version + hash.
func (s *Store) updateTypePrompt(id, text string, version int) error {
	ctx, cancel := opCtx()
	defer cancel()

	// Named fields only: the type's runtime knobs and bindings are not this
	// seeder's to touch.
	_, err := s.db.Collection(agentTypesCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "prompt", Value: text},
			{Key: "prompt_version", Value: version},
			{Key: "prompt_hash", Value: promptbundle.ContentHash(text)},
		}}},
	)
	if err != nil {
		return fmt.Errorf("update bundled prompt %q: %w", id, err)
	}
	return nil
}

// PromptState is what a type currently holds for its prompt, baseline included.
type PromptState struct {
	Text    string
	Hash    string
	Version int
}

// ListPromptStates reads the prompt baseline of every agent type, keyed by id.
//
// Projected: a prompt is the largest field a type has, and the admin page asks
// for this on every load.
func (s *Store) ListPromptStates() (map[string]PromptState, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(agentTypesCollection).Find(ctx, bson.D{},
		options.Find().SetProjection(bson.D{
			{Key: "prompt", Value: 1},
			{Key: "prompt_hash", Value: 1},
			{Key: "prompt_version", Value: 1},
		}))
	if err != nil {
		return nil, fmt.Errorf("list prompt states: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := map[string]PromptState{}
	for cur.Next(ctx) {
		var row struct {
			ID      string `bson:"_id"`
			Prompt  string `bson:"prompt"`
			Hash    string `bson:"prompt_hash"`
			Version int    `bson:"prompt_version"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, fmt.Errorf("decode prompt state: %w", err)
		}
		out[row.ID] = PromptState{Text: row.Prompt, Hash: row.Hash, Version: row.Version}
	}
	return out, cur.Err()
}

// ResetTypePrompt puts the bundled text back over whatever the type holds,
// baseline included. This is the admin's "restore the built-in prompt" action:
// unlike SeedPlatformPrompts it ignores an admin edit, and it never creates a
// type — a reset on a missing id reports "not found" rather than seating a row
// with no execution attributes.
func (s *Store) ResetTypePrompt(id, text string, version int) error {
	ctx, cancel := opCtx()
	defer cancel()

	res, err := s.db.Collection(agentTypesCollection).UpdateOne(ctx,
		bson.D{{Key: "_id", Value: id}},
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "prompt", Value: text},
			{Key: "prompt_version", Value: version},
			{Key: "prompt_hash", Value: promptbundle.ContentHash(text)},
		}}},
	)
	if err != nil {
		return fmt.Errorf("reset prompt of %q: %w", id, err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("agent type %q not found", id)
	}
	return nil
}
