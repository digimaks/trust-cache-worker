package authdoer_test

import (
	"testing"
	"time"

	"azugo.io/core/validation"
	"github.com/go-quicktest/qt"
	"github.com/spf13/viper"

	"github.com/digimaks/trust-cache-worker/internal/authdoer"
)

func TestConfigurationBindDefaults(t *testing.T) {
	v := viper.New()
	(&authdoer.Configuration{}).Bind("trust_auth", v)

	qt.Check(t, qt.Equals(v.GetString("trust_auth.mode"), authdoer.ModeDPoP)) // default dpop — fail toward stronger auth
	qt.Check(t, qt.Equals(v.GetString("trust_auth.audience"), "svc:trust-anchor"))
	qt.Check(t, qt.Equals(v.GetString("trust_auth.scope"), "trust:read"))
	qt.Check(t, qt.Equals(v.GetDuration("trust_auth.timeout"), 15*time.Second))
}

func TestConfigurationValidate(t *testing.T) {
	valid := validation.New()
	tests := []struct {
		name    string
		cfg     authdoer.Configuration
		wantErr bool
	}{
		{
			"internal_ok",
			authdoer.Configuration{Mode: authdoer.ModeInternal, Audience: "svc:trust-anchor", Scope: "trust:read", Timeout: time.Second},
			false,
		},
		{
			"dpop_ok",
			authdoer.Configuration{Mode: authdoer.ModeDPoP, IssuerURL: "http://auth.internal", Audience: "svc:trust-anchor", Scope: "trust:read", ClientID: "cid", ClientSecret: "sec", Timeout: time.Second},
			false,
		},
		{
			"dpop_missing_client_id", // client credentials are required to mint outbound tokens
			authdoer.Configuration{Mode: authdoer.ModeDPoP, IssuerURL: "http://auth.internal", Audience: "svc:trust-anchor", Scope: "trust:read", ClientSecret: "sec", Timeout: time.Second},
			true,
		},
		{
			"dpop_missing_issuer",
			authdoer.Configuration{Mode: authdoer.ModeDPoP, Audience: "svc:trust-anchor", Scope: "trust:read", ClientID: "cid", ClientSecret: "sec", Timeout: time.Second},
			true,
		},
		{
			"unknown_mode_rejected", // never fall through
			authdoer.Configuration{Mode: "basic", Audience: "a", Scope: "s", Timeout: time.Second},
			true,
		},
		{
			"missing_timeout",
			authdoer.Configuration{Mode: authdoer.ModeInternal, Audience: "a", Scope: "s"},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate(valid)
			qt.Check(t, qt.Equals(err != nil, tt.wantErr))
		})
	}
}
