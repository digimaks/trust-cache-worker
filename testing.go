//go:build testhelpers

package trustcacheworker

import (
	"os"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-quicktest/qt"
)

// TestApp builds an App wired to an in-process miniredis and an unreachable
// trust service — sync cycles fail harmlessly, /readyz stays 503, so the
// "not ready before first sync" tests remain deterministic. E2E tests use
// TestAppWith and a mocktrust server instead.
func TestApp(tb testing.TB) *App {
	tb.Helper()
	mr := miniredis.RunT(tb)
	return TestAppWith(tb, "http://127.0.0.1:1", mr.Addr())
}

// TestAppWith builds an App against explicit trust-service + Valkey addresses.
// Auth mode internal: the go-authbyte dpop path needs a live issuer and is
// covered by the authdoer seam tests and integration testing.
func TestAppWith(tb testing.TB, trustURL, valkeyAddr string) *App {
	tb.Helper()
	return TestAppWithPoll(tb, trustURL, valkeyAddr, "1s") // fast loops for E2E assertions
}

// TestAppWithPoll additionally pins the anchor poll interval. A test proving
// the resync route performs a cycle itself uses a long interval, so the
// scheduled loop cannot be the thing that synced. The cache TTL rides along
// (it must exceed the poll interval — config validation enforces it).
func TestAppWithPoll(tb testing.TB, trustURL, valkeyAddr, poll string) *App {
	tb.Helper()

	tb.Setenv("ENVIRONMENT", "development")
	tb.Setenv("SERVICE_NAME", "trust-cache-worker")
	tb.Setenv("METRICS_ENABLED", "false")
	tb.Setenv("VALKEY_URL", valkeyAddr)
	tb.Setenv("TRUST_SERVICE_URL", trustURL)
	tb.Setenv("TRUST_AUTH_MODE", "internal")
	tb.Setenv("TRUST_POLL_INTERVAL", poll)
	if os.Getenv("TRUST_CACHE_TTL") == "" {
		tb.Setenv("TRUST_CACHE_TTL", "48h") // always > any poll a test picks
	}
	tb.Setenv("TRUST_SNAPSHOT_POLL_INTERVAL", "1s")
	tb.Setenv("STATUS_PREFETCH_INTERVAL", "1s")

	app, err := New(nil, "0.0.0-test")
	qt.Assert(tb, qt.IsNil(err))
	return app
}
