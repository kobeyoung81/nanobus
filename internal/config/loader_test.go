package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadBusConfig_EnvVars(t *testing.T) {
	t.Setenv("NANOSEC_BUS_DATABASE_URI", "mongodb://user:pass@127.0.0.2:27017/nanobus")
	t.Setenv("NANOSEC_BUS_SERVER_PORT", "9011")
	t.Setenv("NANOSEC_BUS_SERVER_HOST", "127.0.0.1")
	t.Setenv("NANOSEC_BUS_NANOAUTH_BASE_URL", "http://auth:8081")
	t.Setenv("NANOSEC_BUS_SERVER_RATE_LIMIT_PER_MINUTE", "600")
	t.Setenv("NANOSEC_BUS_SERVER_REPLICAS", "3")

	cfg, err := LoadBusConfig("")
	if err != nil {
		t.Fatalf("LoadBusConfig: %v", err)
	}

	if cfg.Database.URI != "mongodb://user:pass@127.0.0.2:27017/nanobus" {
		t.Errorf("URI not read from env: %q", cfg.Database.URI)
	}
	if cfg.Server.Port != 9011 {
		t.Errorf("port not read from env: %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("host not read from env: %q", cfg.Server.Host)
	}
	if cfg.Nanoauth.BaseURL != "http://auth:8081" {
		t.Errorf("nanoauth base URL not read from env: %q", cfg.Nanoauth.BaseURL)
	}
	if cfg.Server.RateLimitPerMinute != 600 || cfg.Server.Replicas != 3 {
		t.Errorf("rate budget not read from env: %d over %d replicas",
			cfg.Server.RateLimitPerMinute, cfg.Server.Replicas)
	}
}

func TestLoadBusConfig_Defaults(t *testing.T) {
	// Ensure no NANOSEC_BUS_* env leaks into this test from the environment.
	for _, key := range []string{
		"NANOSEC_BUS_DATABASE_URI",
		"NANOSEC_BUS_SERVER_HOST",
		"NANOSEC_BUS_SERVER_PORT",
		"NANOSEC_BUS_NANOAUTH_BASE_URL",
		"NANOSEC_BUS_SERVER_RATE_LIMIT_PER_MINUTE",
		"NANOSEC_BUS_SERVER_REPLICAS",
	} {
		t.Setenv(key, "")
	}

	cfg, err := LoadBusConfig("")
	if err != nil {
		t.Fatalf("LoadBusConfig: %v", err)
	}

	if cfg.Server.RateLimitPerMinute != 0 {
		t.Errorf("the limiter must be off by default, got %d", cfg.Server.RateLimitPerMinute)
	}
	if cfg.Server.Replicas != 1 {
		t.Errorf("default replicas = %d, want 1", cfg.Server.Replicas)
	}
	if cfg.Server.Host != DefaultBusHost {
		t.Errorf("expected default host %s, got %q", DefaultBusHost, cfg.Server.Host)
	}
	if cfg.Server.Port != DefaultBusPort {
		t.Errorf("expected default port %d, got %d", DefaultBusPort, cfg.Server.Port)
	}
	if cfg.Database.URI != "" {
		t.Errorf("expected empty URI by default, got %q", cfg.Database.URI)
	}
}

func TestLoadBusConfig_FileWithEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bus.json")
	if err := os.WriteFile(path, []byte(`{
		"server": { "host": "10.0.0.1", "port": 9001 },
		"database": { "uri": "mongodb://file:27017/nanobus" }
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("NANOSEC_BUS_DATABASE_URI", "mongodb://env:27017/nanobus")
	t.Setenv("NANOSEC_BUS_SERVER_PORT", "")

	cfg, err := LoadBusConfig(path)
	if err != nil {
		t.Fatalf("LoadBusConfig: %v", err)
	}

	// Env wins over the file for the URI (viper precedence: env > config).
	if cfg.Database.URI != "mongodb://env:27017/nanobus" {
		t.Errorf("expected env URI to override file, got %q", cfg.Database.URI)
	}
	// File values apply when the env var is unset.
	if cfg.Server.Host != "10.0.0.1" {
		t.Errorf("expected file host, got %q", cfg.Server.Host)
	}
	if cfg.Server.Port != 9001 {
		t.Errorf("expected file port, got %d", cfg.Server.Port)
	}
}

// The admin URL decides whether a config write is forwarded at all: empty means
// "refuse", so a loader that silently drops the env var turns every console save
// into a 503. It has no default, which is exactly the case viper's Unmarshal
// drops unless the key is registered.
func TestLoadBusConfigReadsTheAdminURL(t *testing.T) {
	t.Setenv("NANOSEC_BUS_ADMIN_BASE_URL", "http://nanoadmin:8080")
	cfg, err := LoadBusConfig("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Admin.BaseURL != "http://nanoadmin:8080" {
		t.Fatalf("admin base url = %q", cfg.Admin.BaseURL)
	}
}
