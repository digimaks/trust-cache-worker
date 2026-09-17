package authdoer

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gmb-lib/go-authbyte/authclient"

	"github.com/gmb-lib/go-platform-kit/propagation"
)

// authbyteDoer adapts go-authbyte's background service-to-service client
// (Client.DoService) to the Doer seam. go-authbyte owns the whole request
// execution — it must compute a fresh DPoP proof bound to the exact method +
// URL using its own ephemeral key, which it never exposes — so there is no
// "sign this *http.Request, then let a generic client send it" primitive to
// implement RequestSigner against (confirmed via go doc). dpop mode.
type authbyteDoer struct {
	client   *authclient.Client
	audience string
	scope    string
}

// NewAuthbyteDoer builds the production dpop-mode Doer directly (bypassing
// authdoer.New/RequestSigner, since there is no per-request sign primitive to
// wire). audience/scope are this worker's OUTBOUND target (the trust-anchor's
// inbound expectations, cfg.Audience/cfg.Scope — e.g. svc:trust-anchor /
// trust:read), passed as DoService's call-time args every request; they are
// NOT authclient.Configuration.ServiceAudience, which is this worker's OWN
// inbound audience and unused (no inbound API here).
func NewAuthbyteDoer(cfg Configuration) (Doer, error) {
	client, err := authclient.New(&authclient.Configuration{
		IssuerURL:           cfg.IssuerURL,
		ServiceAudience:     cfg.Audience, // unused for outbound calls; field is required non-empty
		ServiceClientID:     cfg.ClientID,
		ServiceClientSecret: cfg.ClientSecret,

		// Inbound-only knobs this worker never exercises (no Authenticate()
		// middleware is ever installed) but authclient.Configuration.Validate
		// requires unconditionally. Fixed, not operator-configurable.
		JWKSCacheTTL:             5 * time.Minute,
		ServiceTokenEarlyRefresh: 30 * time.Second,
		DPoPProofMaxAge:          5 * time.Minute,
		DPoPReplayBackend:        authclient.ReplayBackendMemory,
		DPoPNonceTTL:             5 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("authdoer: go-authbyte client: %w", err)
	}
	return &authbyteDoer{client: client, audience: cfg.Audience, scope: cfg.Scope}, nil
}

func (d *authbyteDoer) Do(req *http.Request) (*http.Response, error) {
	if cid := propagation.CorrelationID(req.Context()); cid != "" {
		req.Header.Set(propagation.HeaderCorrelationID, cid)
	}
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("authdoer: read request body: %w", err)
		}
	}
	resp, err := d.client.DoService(req.Context(), d.audience, d.scope, req.Method, req.URL.String(), req.Header, body)
	if err != nil {
		return nil, fmt.Errorf("authdoer: authbyte DoService: %w", err)
	}
	return &http.Response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       io.NopCloser(bytes.NewReader(resp.Body)),
		Request:    req,
	}, nil
}
