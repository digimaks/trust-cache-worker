package trustsync

import (
	"time"

	"azugo.io/core/validation"
	"github.com/spf13/viper"

	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

// Configuration is the trust_sync sub-configuration.
type Configuration struct {
	// PollInterval is the per-type anchors poll cadence. Default 5 m —
	// cheap while unchanged content answers 304, which is the common case.
	// A short poll narrows the propagation window but never closes it; the
	// operator control for "now" is POST /v1/resync.
	PollInterval time.Duration `mapstructure:"poll_interval" validate:"required,gt=0"`

	// CacheTTL is the Valkey validity horizon of materialized anchors: the
	// failover budget eudi-verifier-core may serve from a dead worker before
	// failing closed. Default 45 m = 9 missed polls. Must exceed PollInterval
	// (gtfield) — otherwise a healthy, successfully-polling worker would let
	// its own cache expire every cycle.
	CacheTTL time.Duration `mapstructure:"cache_ttl" validate:"required,gt=0,gtfield=PollInterval"`

	// SnapshotPollInterval drives the freshness telemetry poll.
	SnapshotPollInterval time.Duration `mapstructure:"snapshot_poll_interval" validate:"required,gt=0"`

	// Types are the wire anchor-type names to sync. Default: full taxonomy.
	Types []string `mapstructure:"types" validate:"required,min=1"`
}

// Bind registers defaults and environment-variable bindings with viper.
func (c *Configuration) Bind(prefix string, v *viper.Viper) {
	v.SetDefault(prefix+".poll_interval", 5*time.Minute)
	v.SetDefault(prefix+".cache_ttl", 45*time.Minute)
	v.SetDefault(prefix+".snapshot_poll_interval", 5*time.Minute)
	v.SetDefault(prefix+".types", trustcache.AllTypes())

	_ = v.BindEnv(prefix+".poll_interval", "TRUST_POLL_INTERVAL")
	_ = v.BindEnv(prefix+".cache_ttl", "TRUST_CACHE_TTL")
	_ = v.BindEnv(prefix+".snapshot_poll_interval", "TRUST_SNAPSHOT_POLL_INTERVAL")
	_ = v.BindEnv(prefix+".types", "TRUST_SYNC_TYPES")
}

// Validate checks the struct constraints, then that every configured anchor
// type name is known (unknown type = config error at boot, fail closed).
func (c *Configuration) Validate(valid *validation.Validate) error {
	if err := valid.Struct(c); err != nil {
		return err
	}
	_, err := ParseTypes(c.Types) // unknown type = config error, fail at boot
	return err
}
