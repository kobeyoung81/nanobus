// Package config defines configuration for the nanobus service.
package config

// BusConfig holds configuration for the bus service.
type BusConfig struct {
	Server   ServerConfig   `json:"server"   mapstructure:"server"`
	Database DatabaseConfig `json:"database" mapstructure:"database"`
	Nanoauth NanoauthConfig `json:"nanoauth" mapstructure:"nanoauth"`
	// Admin is the admin module's base URL. Config writes are forwarded there:
	// this service no longer writes the admin_ collections, it asks their owner.
	Admin AdminConfig `json:"admin" mapstructure:"admin"`
	// Portal is the portal service's base URL. The agent rows and their groups
	// are the portal's; the two writes this service still receives for them are
	// forwarded there.
	Portal PortalConfig `json:"portal" mapstructure:"portal"`
	// Provisioner is the runner's base URL. The provisioning queue is its; the
	// console's container actions are forwarded there.
	Provisioner PortalConfig `json:"provisioner" mapstructure:"provisioner"`
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
	// RateLimitPerMinute bounds one client's requests per minute on the bus's
	// REST surface (0 = off, the default). Callers presenting the internal token
	// are exempt: they are the platform's own services, and throttling them would
	// throttle every user at once. The budget is divided by the replica count so a
	// horizontally scaled deployment keeps one platform-wide number.
	RateLimitPerMinute int `json:"rate_limit_per_minute" mapstructure:"rate_limit_per_minute"`
	// Replicas is how many bus replicas serve the deployment. It exists to divide
	// the rate budget: one global limit, split per process.
	Replicas int `json:"replicas" mapstructure:"replicas"`
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

// AdminConfig holds the admin module's connection settings.
type AdminConfig struct {
	BaseURL string `json:"base_url" mapstructure:"base_url"`
}

// PortalConfig holds the portal service connection settings.
type PortalConfig struct {
	BaseURL string `json:"base_url" mapstructure:"base_url"`
}

// NanoauthConfig holds the upstream nanoauth service connection settings.
type NanoauthConfig struct {
	BaseURL string `json:"base_url" mapstructure:"base_url"`
}
