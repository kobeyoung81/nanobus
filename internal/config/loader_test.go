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
}

func TestLoadBusConfig_Defaults(t *testing.T) {
	// Ensure no NANOSEC_BUS_* env leaks into this test from the environment.
	for _, key := range []string{
		"NANOSEC_BUS_DATABASE_URI",
		"NANOSEC_BUS_SERVER_HOST",
		"NANOSEC_BUS_SERVER_PORT",
		"NANOSEC_BUS_NANOAUTH_BASE_URL",
	} {
		t.Setenv(key, "")
	}

	cfg, err := LoadBusConfig("")
	if err != nil {
		t.Fatalf("LoadBusConfig: %v", err)
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
