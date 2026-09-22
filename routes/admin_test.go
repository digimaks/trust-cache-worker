package routes

import (
	"encoding/json"
	"testing"
	"time"

	"azugo.io/azugo"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-quicktest/qt"
	"github.com/valyala/fasthttp"

	worker "github.com/digimaks/trust-cache-worker"
	"github.com/digimaks/trust-cache-worker/internal/mocktrust"
)

const testAdminKey = "test-admin-key"

// adminTestApp builds a started app with the admin key configured and a poll
// interval long enough that, after the immediate warm-up cycle, the only
// thing that can sync is the resync route itself.
func adminTestApp(t *testing.T) (*azugo.TestApp, *mocktrust.Server) {
	t.Helper()
	mr := miniredis.RunT(t)
	mock := mocktrust.New(t)
	t.Setenv("TRUST_ADMIN_KEY", testAdminKey)
	app := worker.TestAppWithPoll(t, mock.URL, mr.Addr(), "1h")
	qt.Assert(t, qt.IsNil(Init(app)))
	ta := azugo.NewTestApp(app.App)
	ta.Start(t)
	t.Cleanup(ta.Stop)
	return ta, mock
}

// waitReady blocks until /readyz answers 200 (the immediate warm-up cycle
// materialized every configured type) so later assertions start from a known
// synced state.
func waitReady(t *testing.T, ta *azugo.TestApp) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		resp, err := ta.TestClient().Get("/readyz")
		qt.Assert(t, qt.IsNil(err))
		code := resp.StatusCode()
		fasthttp.ReleaseResponse(resp)
		if code == fasthttp.StatusOK {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("readyz still %d", code)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

type statusRespBody struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
	Types  map[string]struct {
		Synced        bool   `json:"synced"`
		SnapshotID    string `json:"snapshotId"`
		FetchedAt     string `json:"fetchedAt"`
		ValidUntil    string `json:"validUntil"`
		UpstreamStale bool   `json:"upstreamStale"`
	} `json:"types"`
}

// Fail-closed first: with NO admin key configured the operator surface does
// not exist at all — 404, never an unauthenticated 200.
func TestOperatorSurfaceUnboundWithoutKey(t *testing.T) {
	t.Setenv("TRUST_ADMIN_KEY", "")
	ta := testApp(t)
	ta.Start(t)
	defer ta.Stop()

	tc := ta.TestClient()
	resp, err := tc.Get("/v1/trust-status", tc.WithHeader("X-API-Key", testAdminKey))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(resp.StatusCode(), fasthttp.StatusNotFound))
	fasthttp.ReleaseResponse(resp)

	resp, err = tc.Post("/v1/resync", nil, tc.WithHeader("X-API-Key", testAdminKey))
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(resp.StatusCode(), fasthttp.StatusNotFound))
	fasthttp.ReleaseResponse(resp)
}

// A wrong or absent key is refused on both routes — 401, before any handler
// logic runs.
func TestOperatorSurfaceRejectsWrongOrMissingKey(t *testing.T) {
	ta, _ := adminTestApp(t)
	tc := ta.TestClient()

	for _, req := range []struct {
		name string
		do   func() (*fasthttp.Response, error)
	}{
		{"status wrong key", func() (*fasthttp.Response, error) {
			return tc.Get("/v1/trust-status", tc.WithHeader("X-API-Key", "wrong"))
		}},
		{"status no key", func() (*fasthttp.Response, error) {
			return tc.Get("/v1/trust-status")
		}},
		{"resync wrong key", func() (*fasthttp.Response, error) {
			return tc.Post("/v1/resync", nil, tc.WithHeader("X-API-Key", "wrong"))
		}},
		{"resync no key", func() (*fasthttp.Response, error) {
			return tc.Post("/v1/resync", nil)
		}},
	} {
		resp, err := req.do()
		qt.Assert(t, qt.IsNil(err), qt.Commentf("%s", req.name))
		qt.Check(t, qt.Equals(resp.StatusCode(), fasthttp.StatusUnauthorized), qt.Commentf("%s", req.name))
		fasthttp.ReleaseResponse(resp)
	}
}

// The identity report: after the warm-up sync every configured type carries
// the mock's snapshot id and freshness.
func TestTrustStatusReportsPerTypeIdentity(t *testing.T) {
	ta, _ := adminTestApp(t)
	waitReady(t, ta)

	tc := ta.TestClient()
	resp, err := tc.Get("/v1/trust-status", tc.WithHeader("X-API-Key", testAdminKey))
	qt.Assert(t, qt.IsNil(err))
	defer fasthttp.ReleaseResponse(resp)
	qt.Assert(t, qt.Equals(resp.StatusCode(), fasthttp.StatusOK))

	var body statusRespBody
	qt.Assert(t, qt.IsNil(json.Unmarshal(resp.Body(), &body)))
	qt.Check(t, qt.IsTrue(body.Ready))
	qt.Assert(t, qt.Not(qt.HasLen(body.Types, 0)))
	for typ, ts := range body.Types {
		qt.Check(t, qt.IsTrue(ts.Synced), qt.Commentf("type %s", typ))
		qt.Check(t, qt.Equals(ts.SnapshotID, "snap-0001"), qt.Commentf("type %s", typ))
		qt.Check(t, qt.Not(qt.Equals(ts.FetchedAt, "")), qt.Commentf("type %s", typ))
		qt.Check(t, qt.Not(qt.Equals(ts.ValidUntil, "")), qt.Commentf("type %s", typ))
	}
}

// The core of the operator control: the trust service moves to a new
// snapshot, and POST /v1/resync — not a restart, not the (hour-away)
// scheduled poll — brings the worker to it and answers with the new
// identity, so the operator's call is a comparison rather than a hope.
func TestResyncPerformsCycleAndAnswersWithNewIdentity(t *testing.T) {
	ta, mock := adminTestApp(t)
	waitReady(t, ta)

	mock.SwapSnapshotID(t, "snap-0002")

	tc := ta.TestClient()
	resp, err := tc.Post("/v1/resync", nil, tc.WithHeader("X-API-Key", testAdminKey))
	qt.Assert(t, qt.IsNil(err))
	defer fasthttp.ReleaseResponse(resp)
	qt.Assert(t, qt.Equals(resp.StatusCode(), fasthttp.StatusOK))

	var body statusRespBody
	qt.Assert(t, qt.IsNil(json.Unmarshal(resp.Body(), &body)))
	qt.Check(t, qt.IsTrue(body.Ready))
	qt.Assert(t, qt.Not(qt.HasLen(body.Types, 0)))
	for typ, ts := range body.Types {
		qt.Check(t, qt.Equals(ts.SnapshotID, "snap-0002"), qt.Commentf("type %s", typ))
	}
}

// A resync against an unreachable trust service still answers — 503, with
// the same identity body showing which types have never synced. The cycle's
// failure is visible, never swallowed into a bare acknowledgement.
func TestResyncAgainstUnreachableUpstreamIs503WithIdentityBody(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("TRUST_ADMIN_KEY", testAdminKey)
	app := worker.TestAppWithPoll(t, "http://127.0.0.1:1", mr.Addr(), "1h")
	qt.Assert(t, qt.IsNil(Init(app)))
	ta := azugo.NewTestApp(app.App)
	ta.Start(t)
	defer ta.Stop()

	tc := ta.TestClient()
	resp, err := tc.Post("/v1/resync", nil, tc.WithHeader("X-API-Key", testAdminKey))
	qt.Assert(t, qt.IsNil(err))
	defer fasthttp.ReleaseResponse(resp)
	qt.Assert(t, qt.Equals(resp.StatusCode(), fasthttp.StatusServiceUnavailable))

	var body statusRespBody
	qt.Assert(t, qt.IsNil(json.Unmarshal(resp.Body(), &body)))
	qt.Check(t, qt.IsFalse(body.Ready))
	qt.Assert(t, qt.Not(qt.HasLen(body.Types, 0)))
	for typ, ts := range body.Types {
		qt.Check(t, qt.IsFalse(ts.Synced), qt.Commentf("type %s", typ))
	}
}
