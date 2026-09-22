package prefetch_test

import (
	"testing"
	"time"

	"azugo.io/core/validation"
	"github.com/go-quicktest/qt"
	"github.com/spf13/viper"

	"github.com/digimaks/trust-cache-worker/internal/prefetch"
)

func TestConfigurationBindDefaults(t *testing.T) {
	v := viper.New()
	(&prefetch.Configuration{}).Bind("status_prefetch", v)

	qt.Check(t, qt.Equals(v.GetInt("status_prefetch.top_n"), 50))
	qt.Check(t, qt.Equals(v.GetDuration("status_prefetch.interval"), time.Minute))
	qt.Check(t, qt.Equals(v.GetDuration("status_prefetch.default_ttl"), 5*time.Minute))
	qt.Check(t, qt.Equals(v.GetDuration("status_prefetch.min_ttl"), 30*time.Second))
	qt.Check(t, qt.Equals(v.GetDuration("status_prefetch.max_ttl"), 24*time.Hour))
	qt.Check(t, qt.Equals(v.GetInt64("status_prefetch.max_bytes"), int64(1<<20)))

	// Confirm the bound keys decode into the real Configuration struct
	// (mapstructure tags), not just readable via viper's flat getters.
	var cfg prefetch.Configuration
	qt.Assert(t, qt.IsNil(v.UnmarshalKey("status_prefetch", &cfg)))
	qt.Check(t, qt.Equals(cfg.TopN, 50))
	qt.Check(t, qt.Equals(cfg.Interval, time.Minute))
	qt.Check(t, qt.Equals(cfg.DefaultTTL, 5*time.Minute))
	qt.Check(t, qt.Equals(cfg.MinTTL, 30*time.Second))
	qt.Check(t, qt.Equals(cfg.MaxTTL, 24*time.Hour))
	qt.Check(t, qt.Equals(cfg.MaxBytes, int64(1<<20)))
}

func TestConfigurationBindEnvOverride(t *testing.T) {
	t.Setenv("STATUS_PREFETCH_TOP_N", "7")

	v := viper.New()
	(&prefetch.Configuration{}).Bind("status_prefetch", v)

	qt.Check(t, qt.Equals(v.GetInt("status_prefetch.top_n"), 7))
}

func TestConfigurationValidate(t *testing.T) {
	valid := validation.New()

	good := prefetch.Configuration{
		TopN: 50, Interval: time.Minute, DefaultTTL: 5 * time.Minute,
		MinTTL: 30 * time.Second, MaxTTL: 24 * time.Hour, MaxBytes: 1 << 20,
	}
	qt.Check(t, qt.IsNil(good.Validate(valid)))

	tests := []struct {
		name string
		cfg  prefetch.Configuration
	}{
		{
			// gtfield=MinTTL: MaxTTL must exceed MinTTL, not merely equal it.
			"max_ttl_not_greater_than_min_ttl",
			func() prefetch.Configuration { c := good; c.MaxTTL = c.MinTTL; return c }(),
		},
		{
			"missing_interval",
			func() prefetch.Configuration { c := good; c.Interval = 0; return c }(),
		},
		{
			"missing_max_bytes",
			func() prefetch.Configuration { c := good; c.MaxBytes = 0; return c }(),
		},
		{
			"negative_top_n",
			func() prefetch.Configuration { c := good; c.TopN = -1; return c }(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			qt.Check(t, qt.IsNotNil(tt.cfg.Validate(valid)))
		})
	}
}
