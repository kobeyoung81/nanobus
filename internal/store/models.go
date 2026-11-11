package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanoprotocol"
)

const (
	modelProvidersCollection = "admin_model_providers"
	modelsCollection         = "admin_models"
)

// Errors the catalog actions report to the console as 404/400 rather than 500.
var (
	ErrModelNotFound    = errors.New("model not found")
	ErrNotPlatformModel = errors.New("not a platform model")
)

// storedModelProvider is a provider document.
type storedModelProvider struct {
	ID    int64  `bson:"_id"`
	Scope string `bson:"scope"`
	// Written even when empty, like the NOT NULL DEFAULT '' column: the list
	// filter compares it, and an absent field would not.
	OwnerUserID string    `bson:"owner_user_id"`
	Name        string    `bson:"name"`
	Kind        string    `bson:"kind"`
	BaseURL     string    `bson:"base_url"`
	APIKey      string    `bson:"api_key,omitempty"`
	CreatedAt   time.Time `bson:"created_at"`
	UpdatedAt   time.Time `bson:"updated_at"`
}

// storedModel is a model document under a provider.
type storedModel struct {
	ID         int64  `bson:"_id"`
	ProviderID int64  `bson:"provider_id"`
	Scope      string `bson:"scope"`
	// As above: compared by the list filter, so always written.
	OwnerUserID     string    `bson:"owner_user_id"`
	Name            string    `bson:"name"`
	Model           string    `bson:"model"`
	ContextWindow   int       `bson:"context_window,omitempty"`
	MaxOutputTokens int       `bson:"max_output_tokens,omitempty"`
	ThinkingEnabled bool      `bson:"thinking_enabled"`
	ThinkingEffort  string    `bson:"thinking_effort,omitempty"`
	Params          string    `bson:"params,omitempty"`
	CreatedAt       time.Time `bson:"created_at"`
	UpdatedAt       time.Time `bson:"updated_at"`
}

// timestampString renders a time the way the SQL's DATE_FORMAT did
// (%Y-%m-%dT%H:%i:%s), because these two definitions carry their timestamps as
// strings on the wire. UTC, like every timestamp this migration writes.
func timestampString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05")
}

func (d storedModelProvider) def() nanoprotocol.ModelProviderDef {
	return nanoprotocol.ModelProviderDef{
		ID: d.ID, Scope: d.Scope, OwnerUserID: d.OwnerUserID, Name: d.Name,
		Kind: d.Kind, BaseURL: d.BaseURL, APIKey: d.APIKey,
		CreatedAt: timestampString(d.CreatedAt), UpdatedAt: timestampString(d.UpdatedAt),
	}
}

func (d storedModel) def() nanoprotocol.ModelDef {
	return nanoprotocol.ModelDef{
		ID: d.ID, ProviderID: d.ProviderID, Scope: d.Scope, OwnerUserID: d.OwnerUserID,
		Name: d.Name, Model: d.Model,
		ContextWindow: d.ContextWindow, MaxOutputTokens: d.MaxOutputTokens,
		ThinkingEnabled: d.ThinkingEnabled, ThinkingEffort: d.ThinkingEffort, Params: d.Params,
		CreatedAt: timestampString(d.CreatedAt), UpdatedAt: timestampString(d.UpdatedAt),
	}
}

// visibleScope is the visibility rule both catalogs use: everyone sees the
// platform rows, a caller also sees their own.
func visibleScope(userID string) bson.D {
	return bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "scope", Value: "platform"}},
		bson.D{{Key: "scope", Value: "user"}, {Key: "owner_user_id", Value: userID}},
	}}}
}

// ownership is the write rule both catalogs use: the owner may write their own
// row, and an empty owner (the admin path) may write a platform one.
func ownership(ownerUserID string) bson.D {
	if ownerUserID == "" {
		return bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: "owner_user_id", Value: ""}},
			bson.D{{Key: "scope", Value: "platform"}},
		}}}
	}
	return bson.D{{Key: "owner_user_id", Value: ownerUserID}}
}

// ======== Model providers ========
func (s *Store) GetModelProvider(id int64) (*nanoprotocol.ModelProviderDef, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedModelProvider
	err := s.db.Collection(modelProvidersCollection).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get provider: %w", err)
	}
	def := doc.def()
	return &def, nil
}
func defaultKind(k string) string {
	if k == "" {
		return "openai"
	}
	return k
}
func (s *Store) listModels(filter bson.D) ([]nanoprotocol.ModelDef, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(modelsCollection).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "scope", Value: 1}, {Key: "name", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	out := []nanoprotocol.ModelDef{}
	for cur.Next(ctx) {
		var doc storedModel
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode model: %w", err)
		}
		out = append(out, doc.def())
	}
	return out, cur.Err()
}

func (s *Store) GetModel(id int64) (*nanoprotocol.ModelDef, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedModel
	err := s.db.Collection(modelsCollection).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get model: %w", err)
	}
	def := doc.def()
	return &def, nil
}

// ListVisibleModels returns every model visible to the user, joined with its
// provider's connection fields so the agent can build a client per model.
func (s *Store) ListVisibleModels(userID string) ([]nanoprotocol.ModelDef, error) {
	models, err := s.listModels(visibleScope(userID))
	if err != nil {
		return nil, fmt.Errorf("list visible models: %w", err)
	}
	if len(models) == 0 {
		return models, nil
	}

	// Two steps instead of the join, and the providers are read once for the
	// whole list rather than per model.
	ids := make([]int64, 0, len(models))
	seen := map[int64]bool{}
	for _, m := range models {
		if !seen[m.ProviderID] {
			seen[m.ProviderID] = true
			ids = append(ids, m.ProviderID)
		}
	}

	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(modelProvidersCollection).Find(ctx,
		bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: ids}}}})
	if err != nil {
		return nil, fmt.Errorf("read model providers: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	providers := map[int64]storedModelProvider{}
	for cur.Next(ctx) {
		var p storedModelProvider
		if err := cur.Decode(&p); err != nil {
			return nil, fmt.Errorf("decode provider: %w", err)
		}
		providers[p.ID] = p
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("iterate model providers: %w", err)
	}

	out := make([]nanoprotocol.ModelDef, 0, len(models))
	for _, m := range models {
		p, ok := providers[m.ProviderID]
		if !ok {
			// The SQL's JOIN was an inner one, so a model whose provider is gone
			// was not listed — hidden rather than handed to a runtime that could
			// not use it.
			continue
		}
		m.ProviderKind = p.Kind
		m.ProviderBaseURL = p.BaseURL
		m.ProviderAPIKey = p.APIKey
		out = append(out, m)
	}
	return out, nil
}
func normalizeEffort(e string) string {
	switch e {
	case "low", "medium", "high":
		return e
	default:
		return ""
	}
}

// ValidParamsJSON reports whether s is empty or valid JSON (object).
func ValidParamsJSON(s string) bool {
	if s == "" {
		return true
	}
	var v map[string]any
	return json.Unmarshal([]byte(s), &v) == nil
}
