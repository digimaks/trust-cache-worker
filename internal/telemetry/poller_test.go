package telemetry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	trust "github.com/gmb-eudi/go-eudi-trust"
	"github.com/go-quicktest/qt"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/dativa-lv/trust-cache-worker/internal/health"
	"github.com/dativa-lv/trust-cache-worker/internal/telemetry"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

type fakeSnapClient struct {
	meta trust.SnapshotMeta
	err  error
}

func (f *fakeSnapClient) Anchors(context.Context, trust.AnchorType, string, string) (trust.AnchorSet, bool, error) {
	return trust.AnchorSet{}, false, errors.New("not used in telemetry tests")
}

func (f *fakeSnapClient) Snapshot(context.Context) (trust.SnapshotMeta, error) {
	return f.meta, f.err
}

type spySnapStore struct {
	key string
	val any
	ttl time.Duration
}

func (s *spySnapStore) SetJSON(_ context.Context, key string, v any, ttl time.Duration) error {
	s.key, s.val, s.ttl = key, v, ttl
	return nil
}

func meta(pending bool) trust.SnapshotMeta {
	next := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	return trust.SnapshotMeta{ // field names per the trust client's SnapshotMeta type
		ID:               "snap-1",
		LOTLSequence:     388,
		PendingBootstrap: pending,
		Territories: []trust.TerritoryStatus{
			{Code: "lv", TLSequence: 51, Stale: true, NextUpdate: &next},
			{Code: "EE", TLSequence: 73, Stale: false},
		},
	}
}

func TestPollWritesSnapshotHealth(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	store := &spySnapStore{}
	state := health.NewState([]string{trustcache.TypePIDProvider}, func() time.Time { return now })
	p := telemetry.New(&fakeSnapClient{meta: meta(false)}, store, state, 15*time.Minute, zap.NewNop(), func() time.Time { return now })

	qt.Assert(t, qt.IsNil(p.Poll(context.Background())))

	qt.Check(t, qt.Equals(store.key, trustcache.SnapshotHealthKey))
	qt.Check(t, qt.Equals(store.ttl, 15*time.Minute)) // 3 x poll interval, passed by the caller
	h, ok := store.val.(trustcache.SnapshotHealth)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.Equals(h.LOTLSequence, uint64(388)))
	qt.Check(t, qt.IsTrue(h.UpstreamStale))
	qt.Check(t, qt.IsFalse(h.PendingBootstrap))
	qt.Check(t, qt.Equals(h.CheckedAt, now))
	// Territory codes normalized to the key contract's convention.
	qt.Check(t, qt.IsTrue(h.Territories["LV"].Stale))
	qt.Check(t, qt.Equals(h.Territories["EE"].TLSequence, uint64(73)))
}

// The pending-bootstrap acceptance: the fixture raises the alert —
// Warn log + state flag (the gauge reads the state; asserted in metrics_test).
func TestPollPendingBootstrapRaisesAlert(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	core, logs := observer.New(zap.WarnLevel)
	state := health.NewState(nil, func() time.Time { return now })
	p := telemetry.New(&fakeSnapClient{meta: meta(true)}, &spySnapStore{}, state, 15*time.Minute, zap.New(core), func() time.Time { return now })

	qt.Assert(t, qt.IsNil(p.Poll(context.Background())))

	entries := logs.FilterMessageSnippet("bootstrap").All()
	qt.Assert(t, qt.Equals(len(entries), 1))
	qt.Check(t, qt.Equals(entries[0].Level, zap.WarnLevel))
}

// Poll failure: nothing written, error surfaces (job logs it; freshness keys
// in Valkey age toward expiry — degradation becomes visible downstream).
func TestPollErrorWritesNothing(t *testing.T) {
	store := &spySnapStore{}
	state := health.NewState(nil, nil)
	p := telemetry.New(&fakeSnapClient{err: errors.New("boom")}, store, state, 15*time.Minute, zap.NewNop(), nil)

	err := p.Poll(context.Background())
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.Equals(store.key, ""))
}
