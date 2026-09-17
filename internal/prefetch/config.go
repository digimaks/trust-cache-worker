package prefetch

import (
	"time"

	"azugo.io/core/validation"
	"github.com/spf13/viper"
)

// Configuration is the status_prefetch sub-configuration.
type Configuration struct {
	// TopN status-list URIs (by recent reference) refreshed ahead of TTL.
	// 0 disables prefetching.
	TopN int `mapstructure:"top_n" validate:"gte=0"`

	// Interval between prefetch cycles; an entry whose remaining TTL exceeds
	// the interval is a cache hit (it survives until the next cycle).
	Interval time.Duration `mapstructure:"interval" validate:"required,gt=0"`

	// DefaultTTL applies when the token carries no readable ttl/exp claim
	// (including CWT-form lists — see PeekTTL).
	DefaultTTL time.Duration `mapstructure:"default_ttl" validate:"required,gt=0"`

	// MinTTL/MaxTTL clamp the claimed ttl — an attacker-controlled claim must
	// not pin garbage forever nor thrash the cache.
	MinTTL time.Duration `mapstructure:"min_ttl" validate:"required,gt=0"`
	MaxTTL time.Duration `mapstructure:"max_ttl" validate:"required,gtfield=MinTTL"`

	// MaxBytes caps a fetched status list (decompression happens later, in
	// go-statuslist, with its own caps).
	MaxBytes int64 `mapstructure:"max_bytes" validate:"required,gt=0"`
}

// Bind registers defaults and environment-variable bindings with viper.
func (c *Configuration) Bind(prefix string, v *viper.Viper) {
	v.SetDefault(prefix+".top_n", 50)
	v.SetDefault(prefix+".interval", time.Minute)
	v.SetDefault(prefix+".default_ttl", 5*time.Minute)
	v.SetDefault(prefix+".min_ttl", 30*time.Second)
	v.SetDefault(prefix+".max_ttl", 24*time.Hour)
	v.SetDefault(prefix+".max_bytes", int64(1<<20))

	_ = v.BindEnv(prefix+".top_n", "STATUS_PREFETCH_TOP_N")
	_ = v.BindEnv(prefix+".interval", "STATUS_PREFETCH_INTERVAL")
	_ = v.BindEnv(prefix+".default_ttl", "STATUS_PREFETCH_DEFAULT_TTL")
	_ = v.BindEnv(prefix+".min_ttl", "STATUS_PREFETCH_MIN_TTL")
	_ = v.BindEnv(prefix+".max_ttl", "STATUS_PREFETCH_MAX_TTL")
	_ = v.BindEnv(prefix+".max_bytes", "STATUS_PREFETCH_MAX_BYTES")
}

// Validate checks the struct constraints.
func (c *Configuration) Validate(valid *validation.Validate) error {
	return valid.Struct(c)
}
