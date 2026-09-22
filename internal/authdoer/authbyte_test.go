package authdoer_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/gmb-lib/go-platform-kit/propagation"

	"github.com/digimaks/trust-cache-worker/internal/authdoer"
)

func dpopCfg() authdoer.Configuration {
	return authdoer.Configuration{
		Mode: authdoer.ModeDPoP, IssuerURL: "http://127.0.0.1:1", Audience: "svc:trust-anchor",
		Scope: "trust:read", ClientID: "cid", ClientSecret: "secret", Timeout: 2 * time.Second,
	}
}

// NewAuthbyteDoer builds a production dpop Doer without any network call —
// go-authbyte acquires the client-credentials service token lazily on the
// first DoService, so construction is offline-safe.
func TestNewAuthbyteDoerConstructs(t *testing.T) {
	d, err := authdoer.NewAuthbyteDoer(dpopCfg())
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsNotNil(d))
}

// Do stamps the correlation id, reads the body, and delegates to DoService.
// With an unreachable issuer the client-credentials token mint fails, so Do
// returns an error and nothing is sent unauthenticated (fail closed). The live
// success path needs a real issuer and lands in the compose stack.
func TestAuthbyteDoerDoErrorsWhenIssuerUnreachable(t *testing.T) {
	d, err := authdoer.NewAuthbyteDoer(dpopCfg())
	qt.Assert(t, qt.IsNil(err))
	ctx := propagation.WithCorrelationID(context.Background(), "01CYCLE")

	t.Run("no_body", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://trust.internal/v1/snapshot", nil)
		qt.Assert(t, qt.IsNil(err))
		_, err = d.Do(req)
		qt.Check(t, qt.IsNotNil(err))
	})

	t.Run("with_body", func(t *testing.T) { // exercises the io.ReadAll(req.Body) branch
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://trust.internal/v1/match", strings.NewReader(`{}`))
		qt.Assert(t, qt.IsNil(err))
		_, err = d.Do(req)
		qt.Check(t, qt.IsNotNil(err))
	})
}
