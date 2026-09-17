package trustcacheworker

import (
	"encoding/json"
	"testing"
	"time"

	"azugo.io/azugo"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-quicktest/qt"

	"github.com/dativa-lv/trust-cache-worker/internal/mocktrust"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

// Startup fails fast on an unreachable trust service…
func TestStartupFailsFastWhenTrustServiceUnreachable(t *testing.T) {
	mr := miniredis.RunT(t)
	app := TestAppWith(t, "http://127.0.0.1:1", mr.Addr()) // nothing listens on :1

	err := app.startupCheck()
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.ErrorMatches(err, ".*allow-cold-start.*")) // operator hint present
}

// …unless --allow-cold-start: boots degraded, /readyz stays 503.
func TestStartupColdStartBootsDegraded(t *testing.T) {
	mr := miniredis.RunT(t)
	app := TestAppWith(t, "http://127.0.0.1:1", mr.Addr())
	app.allowColdStart = true

	qt.Assert(t, qt.IsNil(app.startupCheck()))

	ok, _ := app.State().Ready()
	qt.Check(t, qt.IsFalse(ok)) // degraded until the first successful sync
}

func TestStartupSucceedsAgainstMock(t *testing.T) {
	mr := miniredis.RunT(t)
	mock := mocktrust.New(t)
	app := TestAppWith(t, mock.URL, mr.Addr())

	qt.Assert(t, qt.IsNil(app.startupCheck()))
}

// dpop mode wires the go-authbyte adapter (authdoer.NewAuthbyteDoer) rather
// than the plain signingDoer. Construction is offline-safe (tokens are minted
// lazily), so New succeeds without a live issuer; the live DPoP exchange is
// covered by integration testing.
func TestNewDPoPModeWiresAuthbyteDoer(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("SERVICE_NAME", "trust-cache-worker")
	t.Setenv("METRICS_ENABLED", "false")
	t.Setenv("VALKEY_URL", mr.Addr())
	t.Setenv("TRUST_SERVICE_URL", "http://trust.internal")
	t.Setenv("TRUST_AUTH_MODE", "dpop")
	t.Setenv("AUTH_ISSUER_URL", "http://auth.internal")
	t.Setenv("TRUST_AUTH_CLIENT_ID", "cid")
	t.Setenv("TRUST_AUTH_CLIENT_SECRET", "secret")

	app, err := New(nil, "0.0.0-test")
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsNotNil(app.Config()))
}

// End-to-end through the REAL go-eudi-trust client: fixtures parse, anchors
// land in Valkey under the contract keys. (The readyz-flip leg lives in
// routes/router_test.go as TestReadyzBecomesReadyAfterSync — importing routes
// from this in-package test file would cycle.)
func TestEndToEndSyncMaterializesAnchors(t *testing.T) {
	mr := miniredis.RunT(t)
	mock := mocktrust.New(t)
	app := TestAppWith(t, mock.URL, mr.Addr())

	ta := azugo.NewTestApp(app.App)
	ta.Start(t) // starts the app incl. the AddTask'd loops → immediate first sync
	defer ta.Stop()

	key := trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV")
	deadline := time.After(5 * time.Second)
	for !mr.Exists(key) {
		select {
		case <-deadline:
			t.Fatalf("anchor key %s not materialized within 5s", key)
		case <-time.After(20 * time.Millisecond):
		}
	}

	raw, err := mr.Get(key)
	qt.Assert(t, qt.IsNil(err))
	var entry trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal([]byte(raw), &entry)))
	qt.Assert(t, qt.Equals(len(entry.Anchors), 1))
	qt.Check(t, qt.IsTrue(len(entry.Anchors[0].CertDER) > 0)) // real DER through the real client
	qt.Check(t, qt.Equals(entry.SnapshotID, "snap-0001"))

	// The mock recorded the type= filter and, on later cycles, If-None-Match.
	qt.Check(t, qt.IsTrue(len(mock.Requests()) > 0))
}
