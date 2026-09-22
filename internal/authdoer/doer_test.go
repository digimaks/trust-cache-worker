package authdoer_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/gmb-lib/go-platform-kit/propagation"

	"github.com/digimaks/trust-cache-worker/internal/authdoer"
	"github.com/digimaks/trust-cache-worker/internal/mocktrust"
)

func cfg(mode string) authdoer.Configuration {
	return authdoer.Configuration{
		Mode: mode, IssuerURL: "http://auth.internal", Audience: "svc:trust-anchor",
		Scope: "trust:read", Timeout: 5 * time.Second,
	}
}

// fakeSigner stands in for the go-authbyte adapter behind the RequestSigner
// seam: it attaches a DPoP-bound token + proof the way authclient does.
type fakeSigner struct{ err error }

func (f fakeSigner) Sign(req *http.Request) error {
	if f.err != nil {
		return f.err
	}
	req.Header.Set("Authorization", "DPoP test-service-token")
	req.Header.Set("DPoP", "eyJ0eXAiOiJkcG9wK2p3dCJ9.e30.sig")
	return nil
}

func get(ctx context.Context, t *testing.T, d authdoer.Doer, url string) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	qt.Assert(t, qt.IsNil(err))
	resp, err := d.Do(req)
	qt.Assert(t, qt.IsNil(err))
	_ = resp.Body.Close()
}

// Acceptance: dpop mode produces a DPoP-signed request at the mock.
func TestDPoPModeSignsRequests(t *testing.T) {
	mock := mocktrust.New(t)
	d, err := authdoer.New(cfg(authdoer.ModeDPoP), fakeSigner{})
	qt.Assert(t, qt.IsNil(err))

	get(context.Background(), t, d, mock.URL+"/v1/snapshot")

	reqs := mock.Requests()
	qt.Assert(t, qt.Equals(len(reqs), 1))
	qt.Check(t, qt.Equals(reqs[0].Authorization, "DPoP test-service-token"))
	qt.Check(t, qt.Not(qt.Equals(reqs[0].DPoP, ""))) // DPoP proof header present
}

// Acceptance: internal mode is wireable and sends NO auth material
// (same-namespace network trust — dual-auth design).
func TestInternalModeSendsNoAuthHeaders(t *testing.T) {
	mock := mocktrust.New(t)
	d, err := authdoer.New(cfg(authdoer.ModeInternal), nil)
	qt.Assert(t, qt.IsNil(err))

	get(context.Background(), t, d, mock.URL+"/v1/snapshot")

	reqs := mock.Requests()
	qt.Assert(t, qt.Equals(len(reqs), 1))
	qt.Check(t, qt.Equals(reqs[0].Authorization, ""))
	qt.Check(t, qt.Equals(reqs[0].DPoP, ""))
}

// Background cycles propagate their correlation id to the trust service.
func TestCorrelationIDPropagated(t *testing.T) {
	mock := mocktrust.New(t)
	d, err := authdoer.New(cfg(authdoer.ModeInternal), nil)
	qt.Assert(t, qt.IsNil(err))

	ctx := propagation.WithCorrelationID(context.Background(), "01CYCLE")
	get(ctx, t, d, mock.URL+"/v1/snapshot")

	qt.Check(t, qt.Equals(mock.Requests()[0].CorrelationID, "01CYCLE"))
}

// Misconfiguration fails closed at construction, not at first request.
func TestModeWiring(t *testing.T) {
	_, err := authdoer.New(cfg(authdoer.ModeDPoP), nil) // dpop without signer
	qt.Check(t, qt.IsNotNil(err))

	_, err = authdoer.New(cfg(authdoer.ModeInternal), fakeSigner{}) // internal with signer
	qt.Check(t, qt.IsNotNil(err))

	_, err = authdoer.New(cfg("basic"), nil) // unknown mode = reject, never fall through
	qt.Check(t, qt.IsNotNil(err))
}

func TestSignerErrorAborts(t *testing.T) {
	mock := mocktrust.New(t)
	d, err := authdoer.New(cfg(authdoer.ModeDPoP), fakeSigner{err: errors.New("no token")})
	qt.Assert(t, qt.IsNil(err))

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, mock.URL+"/v1/snapshot", nil)
	_, err = d.Do(req)
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.Equals(len(mock.Requests()), 0)) // never sent unsigned
}
