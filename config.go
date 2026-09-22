package trustcacheworker

import (
	pkconfig "github.com/gmb-lib/go-platform-kit/config"

	azugocfg "azugo.io/azugo/config"
	corecfg "azugo.io/core/config"
	"azugo.io/core/validation"
	"github.com/spf13/viper"

	"github.com/digimaks/trust-cache-worker/internal/authdoer"
	"github.com/digimaks/trust-cache-worker/internal/prefetch"
	"github.com/digimaks/trust-cache-worker/internal/trustsync"
)

// Configuration embeds the go-platform-kit base configuration (standard fleet
// env: SERVICE_NAME, ENVIRONMENT, LOG_*, METRICS_ENABLED, OTEL_*). The
// trust_sync / status_prefetch / trust_auth sub-configurations add the
// worker's own settings.
type Configuration struct {
	*pkconfig.BaseConfiguration `mapstructure:",squash"`

	// ValkeyURL is the shared Valkey/Redis as a redis:// or rediss:// URL
	// (user, database index and TLS options ride in the URL); a bare
	// host:port is still accepted. Required — the worker exists to
	// materialize this cache.
	ValkeyURL string `mapstructure:"valkey_url" validate:"required"`

	// ValkeyPassword, when set, overrides the password in ValkeyURL. Also
	// readable through the VALKEY_PASSWORD_FILE indirection so a platform can
	// mount it. Secret — must never appear in logs, events, or error messages.
	ValkeyPassword string `mapstructure:"valkey_password"`

	// ValkeyKeyPrefix is prepended (as "<prefix>:") to every cache key. Set it
	// when the instance confines this user to a key pattern; every service
	// sharing the cache must carry the same value. Empty = no prefix.
	ValkeyKeyPrefix string `mapstructure:"valkey_key_prefix"`

	// TrustServiceURL is the base URL of the central trust-anchor service.
	TrustServiceURL string `mapstructure:"trust_service_url" validate:"required,url"`

	// Sync configures the anchor sync loop.
	Sync *trustsync.Configuration `mapstructure:"trust_sync"`

	// Prefetch configures the status-list prefetch loop.
	Prefetch *prefetch.Configuration `mapstructure:"status_prefetch"`

	// Auth configures the outbound auth doer toward the trust service
	// (TRUST_AUTH_MODE dpop|internal).
	Auth *authdoer.Configuration `mapstructure:"trust_auth"`

	// TrustAdminKey is the constant-time-compared X-API-Key value guarding
	// the operator surface (GET /v1/trust-status, POST /v1/resync) — the same
	// header shape the trust service's own refresh endpoint uses, so one
	// runbook covers both hops. Empty (the default) binds no /v1 routes at
	// all: the operator surface fails closed to nonexistent rather than
	// open. Secret — must never appear in logs, events, or error messages.
	TrustAdminKey string `mapstructure:"trust_admin_key"`
}

// NewConfiguration builds a Configuration with the go-platform-kit base
// configuration embedded, ready for Bind.
func NewConfiguration() *Configuration {
	return &Configuration{BaseConfiguration: pkconfig.New()}
}

// Bind registers defaults and environment-variable bindings with viper.
func (c *Configuration) Bind(_ string, v *viper.Viper) {
	c.BaseConfiguration.Bind("", v) // standard fleet env first

	_ = v.BindEnv("valkey_url", "VALKEY_URL")
	loadSecret(v, "valkey_password", "VALKEY_PASSWORD")
	_ = v.BindEnv("valkey_password", "VALKEY_PASSWORD")
	_ = v.BindEnv("valkey_key_prefix", "VALKEY_KEY_PREFIX")
	_ = v.BindEnv("trust_service_url", "TRUST_SERVICE_URL")
	loadSecret(v, "trust_admin_key", "TRUST_ADMIN_KEY")
	_ = v.BindEnv("trust_admin_key", "TRUST_ADMIN_KEY")

	c.Sync = azugocfg.Bind(c.Sync, "trust_sync", v)
	c.Prefetch = azugocfg.Bind(c.Prefetch, "status_prefetch", v)
	c.Auth = azugocfg.Bind(c.Auth, "trust_auth", v)
}

// loadSecret seeds a config key from the standard secret sources (the plain
// environment variable or its _FILE indirection) so a deployment can mount
// the value instead of inlining it.
func loadSecret(v *viper.Viper, key, name string) {
	if secret, err := corecfg.LoadRemoteSecret(name); err == nil && secret != "" {
		v.SetDefault(key, secret)
	}
}

// Validate validates the base configuration, then this service's own fields.
func (c *Configuration) Validate(valid *validation.Validate) error {
	if err := c.BaseConfiguration.Validate(valid); err != nil {
		return err
	}
	if err := c.Sync.Validate(valid); err != nil {
		return err
	}
	if err := c.Prefetch.Validate(valid); err != nil {
		return err
	}
	if err := c.Auth.Validate(valid); err != nil {
		return err
	}
	return valid.Struct(c)
}
