package server

import "testing"

func TestPreserveMaskedHeaders(t *testing.T) {
	stored := map[string]string{"Authorization": "Bearer real", "X-Old": "keep"}
	incoming := map[string]string{
		"Authorization": maskValue,
		"X-New":         "fresh",
		"X-Gone":        maskValue,
	}
	preserveMaskedHeaders(incoming, stored)

	if got := incoming["Authorization"]; got != "Bearer real" {
		t.Fatalf("masked header not restored: %q", got)
	}
	if got := incoming["X-New"]; got != "fresh" {
		t.Fatalf("unmasked header should pass through: %q", got)
	}
	if _, ok := incoming["X-Gone"]; ok {
		t.Fatalf("masked header with no stored value should be dropped")
	}
}
