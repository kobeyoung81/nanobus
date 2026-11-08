// Package config defines configuration for the nanobus service.
package config

// BusConfig holds configuration for the bus service.
type BusConfig struct {
	Server   ServerConfig   `json:"server"   mapstructure:"server"`
	Database DatabaseConfig `json:"database" mapstructure:"database"`
	Nanoauth NanoauthConfig `json:"nanoauth" mapstructure:"nanoauth"`
	// InternalToken is the shared secret the platform's own callers present on
	// the bus's internal endpoints (NANOSEC_BUS_INTERNAL_TOKEN). It is empty on
	// a deployment that has not been given one, and the bus then refuses those
	// requests rather than serving them unauthenticated.
	InternalToken string `json:"internal_token" mapstructure:"internal_token"`
}

// ServerConfig holds WebSocket server settings.
type ServerConfig struct {
	Host string `json:"host" mapstructure:"host"`
	Port int    `json:"port" mapstructure:"port"`
	// MaxContentBytes bounds a single message's content. Writes above it are
	// rejected at the API with a clear error rather than by the store.
	MaxContentBytes int `json:"max_content_bytes" mapstructure:"max_content_bytes"`
}

// DatabaseConfig holds database connection settings.
//
// MongoDB is the bus's only store since the migration completed (see
// plans/nanobus-mongodb-migration.md).
type DatabaseConfig struct {
	// URI is the MongoDB connection string, and must name the database the bus
	// owns: mongodb://user:pass@host:27017/nanobus?authSource=admin
	URI string `json:"uri" mapstructure:"uri"`
}

// NanoauthConfig holds the upstream nanoauth service connection settings.
type NanoauthConfig struct {
	BaseURL string `json:"base_url" mapstructure:"base_url"`
}
