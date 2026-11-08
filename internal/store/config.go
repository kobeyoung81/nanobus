package store

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gitee.com/kobeyoung81/nanoprotocol"
)

// ConfigItem mirrors nanoprotocol.ConfigItem for store operations.
type ConfigItem = nanoprotocol.ConfigItem

const configItemsCollection = "service_config_items"

// storedConfigItem is the document shape.
//
// It is deliberately not ConfigItem: that type is shared with the protocol and
// carries only json tags, so decoding straight into it would depend on how the
// driver lowercases Go field names ("ValueType" → "valuetype") rather than on
// what the documents actually hold. One explicit mapping beats a silent
// coincidence.
type storedConfigItem struct {
	ComponentType string `bson:"component_type"`
	Key           string `bson:"config_key"`
	Value         string `bson:"config_value"`
	ValueType     string `bson:"value_type"`
	Description   string `bson:"description"`
}

func (d storedConfigItem) item() ConfigItem {
	return ConfigItem{Key: d.Key, Value: d.Value, ValueType: d.ValueType, Description: d.Description}
}

// configItemKey is the identity of one item: the pair the unique index covers.
func configItemKey(componentType, key string) bson.D {
	return bson.D{
		{Key: "component_type", Value: componentType},
		{Key: "config_key", Value: key},
	}
}

// GetConfigItems retrieves all configuration items for the given component type.
func (s *Store) GetConfigItems(componentType string) ([]ConfigItem, error) {
	ctx, cancel := opCtx()
	defer cancel()

	cur, err := s.db.Collection(configItemsCollection).Find(ctx,
		bson.D{{Key: "component_type", Value: componentType}},
		// The unique index is (component_type, config_key), so this sort is
		// served by the index rather than by an in-memory sort.
		options.Find().SetSort(bson.D{{Key: "config_key", Value: 1}}),
	)
	if err != nil {
		return nil, fmt.Errorf("query config items: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	items := []ConfigItem{}
	for cur.Next(ctx) {
		var doc storedConfigItem
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode config item: %w", err)
		}
		items = append(items, doc.item())
	}
	return items, cur.Err()
}

// GetConfigItem retrieves a single configuration item.
func (s *Store) GetConfigItem(componentType, key string) (ConfigItem, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc storedConfigItem
	err := s.db.Collection(configItemsCollection).FindOne(ctx, configItemKey(componentType, key)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ConfigItem{}, fmt.Errorf("config item %q.%q not found", componentType, key)
	}
	if err != nil {
		return ConfigItem{}, fmt.Errorf("query config item: %w", err)
	}
	return doc.item(), nil
}

// SetConfigItem updates or inserts a single configuration item's value.
//
// The insert defaults mirror the SQL upsert exactly: a brand new row got
// value_type 'string' and an empty description while an existing one kept
// whatever it had. $setOnInsert is the operator with that meaning, so an
// existing item's value_type and description survive a value change.
func (s *Store) SetConfigItem(componentType, key, value string) error {
	return s.SetConfigItemTyped(componentType, key, value, "", "")
}

// SetConfigItemTyped is the same upsert with the insert defaults spelled out.
//
// The console's editor only carries a value, but a component that owns a key it
// generates itself (nanoauth's JWT keypair) also knows what kind of value it is —
// `secret`, which is what makes the console mask it. Both fields apply on insert
// only, exactly as the SQL's $setOnInsert semantics did.
func (s *Store) SetConfigItemTyped(componentType, key, value, valueType, description string) error {
	ctx, cancel := opCtx()
	defer cancel()

	if valueType == "" {
		valueType = "string"
	}

	// updated_at mirrors the SQL column's ON UPDATE CURRENT_TIMESTAMP: nothing read
	// it until the console started showing "last changed", which is why it is
	// written here rather than left to the reader.
	now := time.Now().UTC()
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "config_value", Value: value},
			{Key: "updated_at", Value: now},
		}},
		{Key: "$setOnInsert", Value: bson.D{
			{Key: "value_type", Value: valueType},
			{Key: "description", Value: description},
		}},
	}
	// The upserted document takes component_type and config_key from the
	// filter's equality conditions, so they are not repeated in $setOnInsert.
	_, err := s.db.Collection(configItemsCollection).UpdateOne(ctx,
		configItemKey(componentType, key), update, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return fmt.Errorf("upsert config item: %w", err)
	}
	return nil
}

// SeedConfigItems inserts default config items without overwriting existing ones.
//
// The "without overwriting" half is the whole point: seeding runs on every start,
// and an operator's edited value must survive it. $setOnInsert is exactly that
// semantic — contrast SetConfigItem, which always writes the value.
func (s *Store) SeedConfigItems(componentType string, items []ConfigItem) error {
	if len(items) == 0 {
		return nil
	}
	ctx, cancel := opCtx()
	defer cancel()
	coll := s.db.Collection(configItemsCollection)

	for _, item := range items {
		// Only the value fields: component_type and config_key are taken from
		// the filter on upsert, and naming them here too risks a path conflict.
		update := bson.D{{Key: "$setOnInsert", Value: bson.D{
			{Key: "config_value", Value: item.Value},
			{Key: "value_type", Value: item.ValueType},
			{Key: "description", Value: item.Description},
			{Key: "updated_at", Value: time.Now().UTC()},
		}}}
		err := retryOnDuplicateUpsert(func() error {
			_, err := coll.UpdateOne(ctx, configItemKey(componentType, item.Key), update,
				options.UpdateOne().SetUpsert(true))
			return err
		})
		if err != nil {
			return fmt.Errorf("seed config item %q.%q: %w", componentType, item.Key, err)
		}
	}
	return nil
}

// ConfigItemsToMap converts a slice of ConfigItem to a key→value map.
func ConfigItemsToMap(items []ConfigItem) map[string]string {
	m := make(map[string]string, len(items))
	for _, item := range items {
		m[item.Key] = item.Value
	}
	return m
}

// ListConfigComponents returns the component types that have any configuration.
//
// The admin config centre's index needs the list of components to show, and it
// has no way to derive it from a single component's items. Sorted, because the
// SQL it replaces ended with ORDER BY component_type.
func (s *Store) ListConfigComponents() ([]string, error) {
	ctx, cancel := opCtx()
	defer cancel()

	res := s.db.Collection(configItemsCollection).Distinct(ctx, "component_type", bson.D{})
	var values []any
	if err := res.Decode(&values); err != nil {
		return nil, fmt.Errorf("list config components: %w", err)
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if name, ok := v.(string); ok && name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ConfigUpdatedAt returns the newest write in a component's configuration, or the
// zero time when it has none. The console shows "last changed" per component.
func (s *Store) ConfigUpdatedAt(componentType string) (time.Time, error) {
	ctx, cancel := opCtx()
	defer cancel()

	var doc struct {
		UpdatedAt time.Time `bson:"updated_at"`
	}
	err := s.db.Collection(configItemsCollection).FindOne(ctx,
		bson.D{{Key: "component_type", Value: componentType}},
		options.FindOne().
			SetProjection(bson.D{{Key: "updated_at", Value: 1}}).
			SetSort(bson.D{{Key: "updated_at", Value: -1}}),
	).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("config updated at: %w", err)
	}
	return doc.UpdatedAt, nil
}

// CountConfigItems counts a component's items whose key matches a prefix/suffix and
// whose value equals the given one (either may be empty, meaning "any").
//
// It exists for the console's "how many channels are enabled" number: the query it
// replaces was `key LIKE 'channels.%.enabled' AND value = 'true'`, which is a
// pattern over keys nobody wants to enumerate.
func (s *Store) CountConfigItems(componentType, keyPrefix, keySuffix, value string) (int, error) {
	ctx, cancel := opCtx()
	defer cancel()

	filter := bson.D{{Key: "component_type", Value: componentType}}
	if keyPrefix != "" || keySuffix != "" {
		pattern := "^" + regexp.QuoteMeta(keyPrefix) + ".*" + regexp.QuoteMeta(keySuffix) + "$"
		filter = append(filter, bson.E{Key: "config_key", Value: bson.D{{Key: "$regex", Value: pattern}}})
	}
	if value != "" {
		filter = append(filter, bson.E{Key: "config_value", Value: value})
	}

	count, err := s.db.Collection(configItemsCollection).CountDocuments(ctx, filter)
	if err != nil {
		return 0, fmt.Errorf("count config items: %w", err)
	}
	return int(count), nil
}
