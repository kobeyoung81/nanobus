package store

import "testing"

func TestConfigItemsToMap(t *testing.T) {
	m := ConfigItemsToMap([]ConfigItem{
		{Key: "a", Value: "1"},
		{Key: "b", Value: "2"},
	})
	if len(m) != 2 || m["a"] != "1" || m["b"] != "2" {
		t.Fatalf("map = %v", m)
	}
}
