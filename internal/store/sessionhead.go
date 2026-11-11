package store

import (
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// Prompt scopes for session_head_prompts.
const (
	PromptScopePlatform = "platform"
	PromptScopeAgent    = "agent"
)

// SessionModeNoteSettingKey is the definition setting the mode switch note
// travels in: the bus folds it into the session agents' settings so nanosec can
// append it to the tail of every request without a protocol change.
const SessionModeNoteSettingKey = "mode_note"

// SessionHeadPrompt is the shared head prompt of a session agent (build and
// plan). The collection is keyed by scope+owner with no mode, which is what makes
// the two modes share one value by construction.
type SessionHeadPrompt struct {
	Scope           string `json:"scope"`
	OwnerID         string `json:"owner_id"`
	Content         string `json:"content"`
	BaselineHash    string `json:"baseline_hash,omitempty"`
	BaselineVersion int    `json:"baseline_version,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

const sessionHeadPromptsCollection = "admin_prompts"

// storedSessionHead is the document shape.
//
// updated_at is a date rather than the column's text: the legacy read rendered it
// as a Go string, so prompt() renders it back the same way and the JSON the API
// returns is unchanged.
type storedSessionHead struct {
	Scope           string    `bson:"scope"`
	OwnerID         string    `bson:"owner_id"`
	Content         string    `bson:"content"`
	BaselineHash    string    `bson:"baseline_hash"`
	BaselineVersion int       `bson:"baseline_version"`
	UpdatedAt       time.Time `bson:"updated_at"`
}

func (d storedSessionHead) prompt() *SessionHeadPrompt {
	return &SessionHeadPrompt{
		Scope:           d.Scope,
		OwnerID:         d.OwnerID,
		Content:         d.Content,
		BaselineHash:    d.BaselineHash,
		BaselineVersion: d.BaselineVersion,
		UpdatedAt:       d.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

// sessionHeadIdentity is the identity of one row: the pair the unique index
// covers. It is also the upsert filter, so an upserted document takes both fields
// from the filter's equality conditions.
func sessionHeadIdentity(scope, ownerID string) bson.D {
	return bson.D{{Key: "scope", Value: scope}, {Key: "owner_id", Value: ownerID}}
}

func (s *Store) sessionHead(scope, ownerID string) (*SessionHeadPrompt, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedSessionHead
	err := s.db.Collection(sessionHeadPromptsCollection).
		FindOne(ctx, sessionHeadIdentity(scope, ownerID)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session head prompt (%s/%s): %w", scope, ownerID, err)
	}
	return doc.prompt(), nil
}

// PlatformSessionHead returns the platform document, or nil when none is stored.
func (s *Store) PlatformSessionHead() (*SessionHeadPrompt, error) {
	return s.sessionHead(PromptScopePlatform, "")
}

// AgentSessionHead returns an agent's override document, or nil when none is
// stored.
func (s *Store) AgentSessionHead(agentID string) (*SessionHeadPrompt, error) {
	return s.sessionHead(PromptScopeAgent, agentID)
}

// EffectiveSessionHead resolves the head prompt for an agent: its own override
// first, then the platform document, then "" (the caller keeps its built-in
// default).
func (s *Store) EffectiveSessionHead(agentID string) (string, error) {
	if agentID != "" {
		own, err := s.AgentSessionHead(agentID)
		if err != nil {
			return "", err
		}
		if own != nil && own.Content != "" {
			return own.Content, nil
		}
	}
	platform, err := s.PlatformSessionHead()
	if err != nil {
		return "", err
	}
	if platform == nil {
		return "", nil
	}
	return platform.Content, nil
}

// upsertSessionHead writes the content and its baseline together, which is what
// keeps "the text" and "the hash of the text as it was seeded" from drifting
// apart. updated_at mirrors the column's ON UPDATE CURRENT_TIMESTAMP.
func (s *Store) upsertSessionHead(scope, ownerID, content, baselineHash string, baselineVersion int) error {
	ctx, cancel := opCtx()
	defer cancel()

	err := retryOnDuplicateUpsert(func() error {
		_, err := s.db.Collection(sessionHeadPromptsCollection).UpdateOne(ctx,
			sessionHeadIdentity(scope, ownerID),
			bson.D{{Key: "$set", Value: bson.D{
				{Key: "content", Value: content},
				{Key: "baseline_hash", Value: baselineHash},
				{Key: "baseline_version", Value: baselineVersion},
				{Key: "updated_at", Value: time.Now().UTC()},
			}}},
			options.UpdateOne().SetUpsert(true))
		return err
	})
	if err != nil {
		return fmt.Errorf("save session head prompt (%s/%s): %w", scope, ownerID, err)
	}
	return nil
}

// IsSessionType reports whether a definition is a switchable session mode — its
// twin is nanosec's Config.isSessionAgent (output=stream + session_binding=shared).
func IsSessionType(t nanoprotocol.AgentTypeDef) bool {
	return t.Output == nanoprotocol.OutputStream && t.SessionBinding == nanoprotocol.SessionBindingShared
}
