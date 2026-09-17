// Package authdoer builds the outbound HTTP doer the go-eudi-trust client is
// wired with (auth via injectable doer — the library stays framework-free; the
// service owns the mode).
package authdoer

import (
	"fmt"
	"net/http"

	"github.com/gmb-lib/go-platform-kit/observability"
	"github.com/gmb-lib/go-platform-kit/propagation"
)

// Doer is the outbound seam (matches go-eudi-trust's injected doer shape).
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// RequestSigner attaches service credentials (Authorization: DPoP <token> +
// DPoP proof header) to one outbound request. Production: the go-authbyte
// adapter (authbyte.go); tests: a fake.
type RequestSigner interface {
	Sign(req *http.Request) error
}

// New builds the doer for the configured mode. Fail closed at construction:
// dpop without a signer (or internal with one) is a wiring bug, not a
// per-request surprise; unknown mode = reject, never fall through.
func New(cfg Configuration, signer RequestSigner) (Doer, error) {
	switch cfg.Mode {
	case ModeDPoP:
		if signer == nil {
			return nil, fmt.Errorf("authdoer: mode dpop requires a request signer")
		}
	case ModeInternal:
		if signer != nil {
			return nil, fmt.Errorf("authdoer: mode internal must not carry a signer")
		}
	default:
		return nil, fmt.Errorf("authdoer: unknown TRUST_AUTH_MODE %q", cfg.Mode)
	}

	// Bespoke background client (no *azugo.Context in a Tasker): the kit's
	// InstrumentHTTPClient keeps OTel client spans + trace propagation; the
	// correlation header is set here because bespoke clients must carry it
	// themselves.
	base := observability.InstrumentHTTPClient(&http.Client{Timeout: cfg.Timeout})
	return &signingDoer{base: base, signer: signer}, nil
}

type signingDoer struct {
	base   *http.Client
	signer RequestSigner
}

func (d *signingDoer) Do(req *http.Request) (*http.Response, error) {
	if cid := propagation.CorrelationID(req.Context()); cid != "" {
		req.Header.Set(propagation.HeaderCorrelationID, cid)
	}
	if d.signer != nil {
		if err := d.signer.Sign(req); err != nil {
			return nil, fmt.Errorf("authdoer: sign request: %w", err)
		}
	}
	// G704 is a false positive here: signingDoer is the go-eudi-trust client's
	// injected transport. It forwards the request the client built from the
	// operator-configured, url-validated TrustServiceURL — not user input.
	// Forwarding is the doer's entire purpose; SSRF filtering is not this
	// layer's job.
	return d.base.Do(req) //nolint:gosec // G704: injected trust-client transport, target is the validated TrustServiceURL
}
