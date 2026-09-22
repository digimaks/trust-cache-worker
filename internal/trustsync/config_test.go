package trustsync_test

import (
	"testing"
	"time"

	"azugo.io/core/validation"
	"github.com/go-quicktest/qt"
	"github.com/spf13/viper"

	"github.com/digimaks/trust-cache-worker/internal/trustsync"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

func TestConfigurationBindDefaults(t *testing.T) {
	v := viper.New()
	(&trustsync.Configuration{}).Bind("trust_sync", v)

	qt.Check(t, qt.Equals(v.GetDuration("trust_sync.poll_interval"), 5*time.Minute))
	qt.Check(t, qt.Equals(v.GetDuration("trust_sync.cache_ttl"), 45*time.Minute))
	qt.Check(t, qt.Equals(v.GetDuration("trust_sync.snapshot_poll_interval"), 5*time.Minute))
	qt.Check(t, qt.DeepEquals(v.GetStringSlice("trust_sync.types"), trustcache.AllTypes()))
	qt.Check(t, qt.Equals(len(v.GetStringSlice("trust_sync.types")), 11))

	// Confirm the bound keys decode into the real Configuration struct.
	var cfg trustsync.Configuration
	qt.Assert(t, qt.IsNil(v.UnmarshalKey("trust_sync", &cfg)))
	qt.Check(t, qt.Equals(cfg.PollInterval, 5*time.Minute))
	qt.Check(t, qt.Equals(cfg.CacheTTL, 45*time.Minute))
	qt.Check(t, qt.Equals(cfg.SnapshotPollInterval, 5*time.Minute))
	qt.Check(t, qt.DeepEquals(cfg.Types, trustcache.AllTypes()))
}

func TestConfigurationBindEnvOverride(t *testing.T) {
	t.Setenv("TRUST_POLL_INTERVAL", "30m")

	v := viper.New()
	(&trustsync.Configuration{}).Bind("trust_sync", v)

	qt.Check(t, qt.Equals(v.GetDuration("trust_sync.poll_interval"), 30*time.Minute))
}

func TestConfigurationValidate(t *testing.T) {
	valid := validation.New()

	good := trustsync.Configuration{
		PollInterval: 15 * time.Minute, CacheTTL: 45 * time.Minute,
		SnapshotPollInterval: 5 * time.Minute, Types: trustcache.AllTypes(),
	}
	qt.Check(t, qt.IsNil(good.Validate(valid)))

	tests := []struct {
		name string
		cfg  trustsync.Configuration
	}{
		{
			// Validate calls ParseTypes internally — an unknown type name must
			// fail configuration at boot (fail closed).
			"unknown_type_name",
			func() trustsync.Configuration { c := good; c.Types = []string{"nonsense"}; return c }(),
		},
		{
			"empty_types",
			func() trustsync.Configuration { c := good; c.Types = nil; return c }(),
		},
		{
			"missing_poll_interval",
			func() trustsync.Configuration { c := good; c.PollInterval = 0; return c }(),
		},
		{
			"missing_cache_ttl",
			func() trustsync.Configuration { c := good; c.CacheTTL = 0; return c }(),
		},
		{
			// CacheTTL must exceed PollInterval: a healthy, successfully-polling
			// worker must never let its own cache expire.
			"cache_ttl_not_greater_than_poll_interval",
			func() trustsync.Configuration { c := good; c.CacheTTL = c.PollInterval; return c }(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qt.Check(t, qt.IsNotNil(tt.cfg.Validate(valid)))
		})
	}
}
