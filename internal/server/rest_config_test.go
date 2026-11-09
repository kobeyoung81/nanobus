package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"gitee.com/kobeyoung81/nanobus/internal/store"
)

// TestRestListConfig pins the admin config index's contract.
//
// It exists because the portal used to build this list by querying
// service_config_items itself. That list is now served here, so its shape is the
// contract between the two services and belongs in a test rather than in a
// reader's memory of the SQL it replaced.
func TestRestListConfig(t *testing.T) {
	s := testServerMongo(t)
	if err := seedConfig(t, s.store, "bus", []store.ConfigItem{{Key: "port", Value: "9000"}}); err != nil {
		t.Fatalf("seed bus: %v", err)
	}
	if err := seedConfig(t, s.store, "agent", []store.ConfigItem{{Key: "model", Value: "m"}}); err != nil {
		t.Fatalf("seed agent: %v", err)
	}

	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Components []string `json:"components"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(payload.Components) != 2 || payload.Components[0] != "agent" || payload.Components[1] != "bus" {
		t.Fatalf("components = %v, want [agent bus]", payload.Components)
	}
}

// TestRestGetConfig_Items pins the envelope a component read returns: the portal
// decodes `items[].key/value` out of it, so a rename here would empty the config
// centre silently.
func TestRestGetConfig_Items(t *testing.T) {
	s := testServerMongo(t)
	if err := seedConfig(t, s.store, "bus", []store.ConfigItem{
		{Key: "port", Value: "9000", ValueType: "int", Description: "Bus port"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config/bus", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		ComponentType string `json:"component_type"`
		Items         []struct {
			Key       string `json:"key"`
			Value     string `json:"value"`
			ValueType string `json:"value_type"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if payload.ComponentType != "bus" {
		t.Fatalf("component_type = %q, want bus", payload.ComponentType)
	}
	if len(payload.Items) != 1 || payload.Items[0].Key != "port" || payload.Items[0].Value != "9000" {
		t.Fatalf("items = %+v", payload.Items)
	}
}

// TestRestGetConfig_UnknownComponentIsEmptyNotAnError pins what an unknown
// component reads as. The portal distinguishes "no configuration" from "the bus
// failed", and only the first should be an empty list.
func TestRestGetConfig_UnknownComponentIsEmptyNotAnError(t *testing.T) {
	s := testServerMongo(t)

	rec := httptest.NewRecorder()
	restMux(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/config/nope", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Items) != 0 {
		t.Fatalf("expected no items, got %d", len(payload.Items))
	}
}
