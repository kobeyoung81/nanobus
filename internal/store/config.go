package store

import "gitee.com/kobeyoung81/nanoprotocol"

// ConfigItem mirrors nanoprotocol.ConfigItem for callers that build a settings
// map from configuration items.
//
// The bus no longer reads or writes this collection: configuration belongs to
// the admin module, which serves it over HTTP. The alias stays because the voice
// settings loader — which receives items from that HTTP client — works on this
// shape.
type ConfigItem = nanoprotocol.ConfigItem

// ConfigItemsToMap converts a slice of ConfigItem to a key→value map.
func ConfigItemsToMap(items []ConfigItem) map[string]string {
	m := make(map[string]string, len(items))
	for _, item := range items {
		m[item.Key] = item.Value
	}
	return m
}
