package authdoer

import (
	"time"

	"azugo.io/core/validation"
	"github.com/spf13/viper"
)

// Auth modes toward the trust service (env-selected dual auth — full DPoP for
// cross-namespace calls, lighter network-trust auth inside one namespace; both
// must be wireable).
const (
	ModeDPoP     = "dpop"
	ModeInternal = "internal"
)

// Configuration is the trust_auth sub-configuration.
type Configuration struct {
	// Mode selects the auth doer: dpop (go-authbyte service token + proof)
	// or internal (same-namespace, no token). Default dpop — fail toward the
	// stronger auth.
	Mode string `mapstructure:"mode" validate:"required,oneof=dpop internal"`

	// IssuerURL is the go-authbyte token issuer (dpop mode only).
	IssuerURL string `mapstructure:"issuer_url" validate:"required_if=Mode dpop,omitempty,url"`

	// Audience/Scope mirror the trust-anchor's inbound expectations
	// (SERVICE_AUDIENCE=svc:trust-anchor; scope trust:read). Passed as
	// DoService's call-time args (authbyte.go), NOT authclient.Configuration's
	// own ServiceAudience (that field is this worker's OWN inbound audience,
	// unused — the worker has no inbound API).
	Audience string `mapstructure:"audience" validate:"required"`
	Scope    string `mapstructure:"scope" validate:"required"`

	// ClientID/ClientSecret authenticate the outbound client-credentials
	// token mint (go-authbyte authclient.Configuration.ServiceClientID/
	// Secret; dpop mode only — real API discovery found no per-request "sign
	// this request" primitive, only a client-credentials-backed DoService
	// call).
	ClientID     string `mapstructure:"client_id" validate:"required_if=Mode dpop"`
	ClientSecret string `mapstructure:"client_secret" validate:"required_if=Mode dpop"`

	// Timeout bounds each outbound trust-service call.
	Timeout time.Duration `mapstructure:"timeout" validate:"required,gt=0"`
}

// Bind registers defaults and environment-variable bindings with viper.
func (c *Configuration) Bind(prefix string, v *viper.Viper) {
	v.SetDefault(prefix+".mode", ModeDPoP)
	v.SetDefault(prefix+".audience", "svc:trust-anchor")
	v.SetDefault(prefix+".scope", "trust:read")
	v.SetDefault(prefix+".timeout", 15*time.Second)

	_ = v.BindEnv(prefix+".mode", "TRUST_AUTH_MODE")
	_ = v.BindEnv(prefix+".issuer_url", "AUTH_ISSUER_URL")
	_ = v.BindEnv(prefix+".audience", "TRUST_AUTH_AUDIENCE")
	_ = v.BindEnv(prefix+".scope", "TRUST_AUTH_SCOPE")
	_ = v.BindEnv(prefix+".client_id", "TRUST_AUTH_CLIENT_ID")
	_ = v.BindEnv(prefix+".client_secret", "TRUST_AUTH_CLIENT_SECRET")
	_ = v.BindEnv(prefix+".timeout", "TRUST_AUTH_TIMEOUT")
}

// Validate checks the struct constraints.
func (c *Configuration) Validate(valid *validation.Validate) error {
	return valid.Struct(c)
}
