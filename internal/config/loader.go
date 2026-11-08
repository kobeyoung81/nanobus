package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

const (
	DefaultBusHost = "0.0.0.0"
	DefaultBusPort = 9000
	// DefaultMaxContentBytes bounds a single message body at 16 MiB - 1, so an
	// oversize message is rejected at the API with a clear error instead of by the
	// store.
	DefaultMaxContentBytes = 16*1024*1024 - 1
)

// DefaultBusConfig returns a BusConfig with sensible defaults.
func DefaultBusConfig() BusConfig {
	return BusConfig{
		Server: ServerConfig{
			Host:            DefaultBusHost,
			Port:            DefaultBusPort,
			MaxContentBytes: DefaultMaxContentBytes,
		},
	}
}

// LoadBusConfig loads bus configuration from file and environment.
//
// Env vars (NANOSEC_BUS_*; dots in keys become underscores) override values
// from the config file. They are bound explicitly: viper's AutomaticEnv only
// affects Get(), not Unmarshal(), so without BindEnv the URI and other keys
// would silently stay empty.
func LoadBusConfig(path string) (BusConfig, error) {
	cfg := DefaultBusConfig()
	v := viper.New()
	v.SetEnvPrefix("NANOSEC_BUS")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	for _, key := range []string{
		"server.host",
		"server.port",
		"server.max_content_bytes",
		"database.uri",
		"nanoauth.base_url",
		"internal_token",
	} {
		v.BindEnv(key)
	}

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return BusConfig{}, fmt.Errorf("read config file %s: %w", path, err)
		}
	}

	cfg.Server.Host = defaultIfUnset(v, "server.host", cfg.Server.Host)
	if v.IsSet("server.port") {
		cfg.Server.Port = v.GetInt("server.port")
	}
	if v.IsSet("server.max_content_bytes") {
		cfg.Server.MaxContentBytes = v.GetInt("server.max_content_bytes")
	}
	cfg.Database.URI = v.GetString("database.uri")
	cfg.Nanoauth.BaseURL = v.GetString("nanoauth.base_url")
	cfg.InternalToken = strings.TrimSpace(v.GetString("internal_token"))
	return cfg, nil
}

// defaultIfUnset returns the current value when viper reports the key as unset,
// so struct defaults survive when neither a config file nor an env var supplies it.
func defaultIfUnset(v *viper.Viper, key, fallback string) string {
	if !v.IsSet(key) {
		return fallback
	}
	return v.GetString(key)
}

// ValidateBusConfig checks that required bus configuration fields are set.
func ValidateBusConfig(cfg BusConfig) error {
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return fmt.Errorf("invalid server port: %d", cfg.Server.Port)
	}
	if cfg.Server.MaxContentBytes <= 0 {
		return fmt.Errorf("invalid server max_content_bytes: %d", cfg.Server.MaxContentBytes)
	}
	if cfg.Database.URI == "" {
		return fmt.Errorf("database URI is required (mongodb://user:pass@host:27017/<db>?…)")
	}
	return nil
}
