package trustsync_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	trust "github.com/gmb-eudi/go-eudi-trust"
	"github.com/go-quicktest/qt"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/dativa-lv/trust-cache-worker/internal/health"
	"github.com/dativa-lv/trust-cache-worker/internal/trustsync"
	"github.com/dativa-lv/trust-cache-worker/internal/valkeystore"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

// --- fakes -----------------------------------------------------------------

type anchorsCall struct {
	t         trust.AnchorType
	territory string
	etag      string
}

// fakeClient scripts trust.Client responses. Field names reconciled against
// go-eudi-trust: AnchorSet's snapshot id is Snapshot (not SnapshotID).
// Safe for concurrent use (the Run join tests call it from two goroutines).
type fakeClient struct {
	set   trust.AnchorSet
	ok    bool // false = 304
	err   error
	calls []anchorsCall

	// errFor scripts a per-type override (SyncAll continue-on-error coverage):
	// when set for a type, that call returns the scripted error instead of the
	// blanket set/ok/err above, while other types still hit the shared script.
	errFor map[trust.AnchorType]error

	// gate, when non-nil, blocks every Anchors call until closed — the Run
	// join tests hold a cycle in flight with it.
	gate chan struct{}

	mu sync.Mutex
}

func (f *fakeClient) Anchors(_ context.Context, t trust.AnchorType, territory, etag string) (trust.AnchorSet, bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, anchorsCall{t: t, territory: territory, etag: etag})
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if f.errFor != nil {
		if err, ok := f.errFor[t]; ok {
			return trust.AnchorSet{}, false, err
		}
	}
	return f.set, f.ok, f.err
}

func (f *fakeClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeClient) Snapshot(context.Context) (trust.SnapshotMeta, error) {
	return trust.SnapshotMeta{}, errors.New("not used in this test")
}

type spyStore struct {
	swaps     []valkeystore.Swap
	refreshes []string
}

func (s *spyStore) SwapAnchorType(_ context.Context, sw valkeystore.Swap) error {
	s.swaps = append(s.swaps, sw)
	return nil
}

func (s *spyStore) RefreshAnchorType(_ context.Context, keyType string, _ trustcache.TypeFreshness, _ time.Duration) error {
	s.refreshes = append(s.refreshes, keyType)
	return nil
}

func anchorLV(t *testing.T) trust.Anchor {
	t.Helper()
	return trust.Anchor{ // Cert field intentionally not set: the worker maps DER + metadata only
		Type: trust.PIDProvider, Country: "LV", Status: "granted",
		ValidUntil: time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC), TLSequence: 51,
	}
}

// --- tests -----------------------------------------------------------------

func TestWireTypeExhaustive(t *testing.T) {
	tests := []struct {
		in   trust.AnchorType
		want string
	}{
		{trust.PIDProvider, trustcache.TypePIDProvider},
		{trust.QEAAProvider, trustcache.TypeQEAAProvider},
		{trust.PubEAAProvider, trustcache.TypePubEAAProvider},
		{trust.EAAProvider, trustcache.TypeEAAProvider},
		{trust.WalletProvider, trustcache.TypeWalletProvider},
		{trust.AccessCA, trustcache.TypeAccessCA},
		{trust.WRPRCIssuer, trustcache.TypeWRPRCIssuer},
		{trust.PIDProviderStatus, trustcache.TypePIDProviderStatus},
		{trust.QEAAProviderStatus, trustcache.TypeQEAAProviderStatus},
		{trust.PubEAAProviderStatus, trustcache.TypePubEAAProviderStatus},
		{trust.EAAProviderStatus, trustcache.TypeEAAProviderStatus},
	}
	for _, tt := range tests {
		got, err := trustsync.WireType(tt.in)
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.Equals(got, tt.want))
	}
	// Unknown type = reject, never fall through (fail-closed posture).
	_, err := trustsync.WireType(trust.AnchorType("bogus"))
	qt.Check(t, qt.IsNotNil(err))
}

func TestParseTypesRejectsUnknown(t *testing.T) {
	_, err := trustsync.ParseTypes([]string{"pid_provider", "nonsense"})
	qt.Check(t, qt.IsNotNil(err))

	got, err := trustsync.ParseTypes([]string{"pid_provider", "access_ca"})
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(len(got), 2))
}

// A changed snapshot swaps; the recorded call carries the remembered ETag.
func TestSyncTypeSwapsOnChangeAndRemembersETag(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	fc := &fakeClient{
		set: trust.AnchorSet{Anchors: []trust.Anchor{anchorLV(t)}, Snapshot: "snap-1", Stale: false, FetchedAt: now},
		ok:  true,
	}
	spy := &spyStore{}
	state := health.NewState([]string{trustcache.TypePIDProvider}, func() time.Time { return now })
	s := trustsync.New(fc, spy, state, trustsync.Configuration{
		PollInterval: 15 * time.Minute, CacheTTL: 45 * time.Minute, Types: []string{trustcache.TypePIDProvider},
	}, zap.NewNop(), func() time.Time { return now })

	qt.Assert(t, qt.IsNil(s.SyncType(context.Background(), trust.PIDProvider)))

	// First poll sends no ETag (If-None-Match is sent on subsequent polls).
	qt.Assert(t, qt.Equals(len(fc.calls), 1))
	qt.Check(t, qt.Equals(fc.calls[0].etag, ""))
	qt.Check(t, qt.Equals(fc.calls[0].territory, "")) // per-type poll covers all territories

	qt.Assert(t, qt.Equals(len(spy.swaps), 1))
	sw := spy.swaps[0]
	qt.Check(t, qt.Equals(sw.KeyType, trustcache.TypePIDProvider))
	qt.Check(t, qt.Equals(sw.ETag, "snap-1"))
	qt.Assert(t, qt.Equals(len(sw.Entries), 1))
	qt.Check(t, qt.Equals(sw.Entries[0].Territory, "LV"))
	qt.Check(t, qt.Equals(sw.Entries[0].ValidUntil.Unix(), now.Add(45*time.Minute).Unix()))

	// Readiness moved to ready for this type.
	ready, _ := state.Ready()
	qt.Check(t, qt.IsTrue(ready))

	// Second poll: 304 — the client is called WITH the remembered ETag and the
	// store sees a refresh, zero swaps (zero data churn).
	fc.ok = false
	qt.Assert(t, qt.IsNil(s.SyncType(context.Background(), trust.PIDProvider)))
	qt.Check(t, qt.Equals(fc.calls[1].etag, "snap-1"))
	qt.Check(t, qt.Equals(len(spy.swaps), 1))
	qt.Assert(t, qt.DeepEquals(spy.refreshes, []string{trustcache.TypePIDProvider}))
}

func TestSyncTypeErrorKeepsStateAndReturnsError(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	fc := &fakeClient{err: errors.New("boom")}
	spy := &spyStore{}
	state := health.NewState([]string{trustcache.TypePIDProvider}, func() time.Time { return now })
	s := trustsync.New(fc, spy, state, trustsync.Configuration{
		PollInterval: 15 * time.Minute, CacheTTL: 45 * time.Minute, Types: []string{trustcache.TypePIDProvider},
	}, zap.NewNop(), func() time.Time { return now })

	err := s.SyncType(context.Background(), trust.PIDProvider)
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.Equals(len(spy.swaps), 0))     // nothing written on failure
	qt.Check(t, qt.Equals(len(spy.refreshes), 0)) // and no TTL extension either — cache ages toward fail-closed
	ready, _ := state.Ready()
	qt.Check(t, qt.IsFalse(ready))
}

// SyncAll must not let one broken type starve the others' freshness: a
// failing AccessCA poll is logged, not aborted, and PIDProvider still swaps.
func TestSyncAllContinuesPastOneTypeFailure(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	fc := &fakeClient{
		set: trust.AnchorSet{Anchors: []trust.Anchor{anchorLV(t)}, Snapshot: "snap-1", FetchedAt: now},
		ok:  true,
		errFor: map[trust.AnchorType]error{
			trust.AccessCA: errors.New("boom"),
		},
	}
	spy := &spyStore{}
	state := health.NewState([]string{trustcache.TypePIDProvider, trustcache.TypeAccessCA}, func() time.Time { return now })
	core, logs := observer.New(zap.ErrorLevel)
	s := trustsync.New(fc, spy, state, trustsync.Configuration{
		PollInterval: 15 * time.Minute, CacheTTL: 45 * time.Minute,
		Types: []string{trustcache.TypePIDProvider, trustcache.TypeAccessCA},
	}, zap.New(core), func() time.Time { return now })

	s.SyncAll(context.Background()) // must return, not panic/block, despite the per-type failure

	// PIDProvider's swap went through despite AccessCA's failure.
	qt.Assert(t, qt.Equals(len(spy.swaps), 1))
	qt.Check(t, qt.Equals(spy.swaps[0].KeyType, trustcache.TypePIDProvider))

	// Both types were attempted (loop didn't abort early); AccessCA's error surfaced as an Error log, not swallowed silently.
	qt.Check(t, qt.Equals(len(fc.calls), 2))
	entries := logs.FilterMessage("anchor sync failed").All()
	qt.Assert(t, qt.Equals(len(entries), 1))
	qt.Check(t, qt.Equals(entries[0].ContextMap()["anchor_type"], trustcache.TypeAccessCA))

	// Overall readiness requires EVERY configured type fresh (health.State
	// posture, fail closed): AccessCA never synced, so the worker as a whole
	// is correctly still not-ready even though PIDProvider's swap landed.
	ready, reason := state.Ready()
	qt.Check(t, qt.IsFalse(ready))
	qt.Check(t, qt.Equals(reason, "anchor type never synced: "+trustcache.TypeAccessCA))
}

// End-to-end against the real store: a withdrawn anchor is gone from Valkey
// immediately after the poll that saw the new snapshot (fixture-driven at the
// trust.Client seam).
func TestSyncPurgesWithdrawnAnchorEndToEnd(t *testing.T) {
	mr := miniredis.RunT(t)
	store, err := valkeystore.New(valkeystore.Options{URL: mr.Addr()})
	qt.Assert(t, qt.IsNil(err))
	defer store.Close()

	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	a := anchorLV(t)
	b := anchorLV(t)
	b.TLSequence = 52 // distinguishable second anchor
	fc := &fakeClient{set: trust.AnchorSet{Anchors: []trust.Anchor{a, b}, Snapshot: "snap-1", FetchedAt: now}, ok: true}
	state := health.NewState([]string{trustcache.TypePIDProvider}, func() time.Time { return now })
	s := trustsync.New(fc, store, state, trustsync.Configuration{
		PollInterval: 15 * time.Minute, CacheTTL: 45 * time.Minute, Types: []string{trustcache.TypePIDProvider},
	}, zap.NewNop(), func() time.Time { return now })

	ctx := context.Background()
	qt.Assert(t, qt.IsNil(s.SyncType(ctx, trust.PIDProvider)))

	// New snapshot without anchor b.
	fc.set = trust.AnchorSet{Anchors: []trust.Anchor{a}, Snapshot: "snap-2", FetchedAt: now}
	start := time.Now()
	qt.Assert(t, qt.IsNil(s.SyncType(ctx, trust.PIDProvider)))
	qt.Check(t, qt.IsTrue(time.Since(start) < 5*time.Second)) // gone < 5 s after poll

	raw, err := store.Get(ctx, trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV"))
	qt.Assert(t, qt.IsNil(err))
	var got trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal(raw, &got)))
	qt.Assert(t, qt.Equals(len(got.Anchors), 1))
	qt.Check(t, qt.Equals(got.Anchors[0].TLSequence, int64(51)))
	qt.Check(t, qt.Equals(got.SnapshotID, "snap-2"))
}

// End-to-end against the real store: the steady-state fail-closed trap fix.
// A 304 is upstream's explicit confirmation the snapshot is still current,
// so the embedded validUntil on every affected trust:anchors:<type>:<terr>
// entry must advance exactly as it would on a 200 — otherwise a deployment
// whose snapshot never changes again after the first poll bricks itself the
// instant that first ValidUntil passes, even though the worker keeps polling
// successfully forever (confirmed empirically on the live stack).
func TestSyncTypeAdvancesEmbeddedValidUntilOn304EndToEnd(t *testing.T) {
	mr := miniredis.RunT(t)
	store, err := valkeystore.New(valkeystore.Options{URL: mr.Addr()})
	qt.Assert(t, qt.IsNil(err))
	defer store.Close()

	clock := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	fc := &fakeClient{set: trust.AnchorSet{Anchors: []trust.Anchor{anchorLV(t)}, Snapshot: "snap-1", FetchedAt: clock}, ok: true}
	state := health.NewState([]string{trustcache.TypePIDProvider}, now)
	s := trustsync.New(fc, store, state, trustsync.Configuration{
		PollInterval: 15 * time.Minute, CacheTTL: 45 * time.Minute, Types: []string{trustcache.TypePIDProvider},
	}, zap.NewNop(), now)

	ctx := context.Background()
	key := trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV")

	qt.Assert(t, qt.IsNil(s.SyncType(ctx, trust.PIDProvider))) // 200: validUntil = 12:00 + 45m

	raw, err := store.Get(ctx, key)
	qt.Assert(t, qt.IsNil(err))
	var first trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal(raw, &first)))
	qt.Check(t, qt.Equals(first.ValidUntil.Unix(), clock.Add(45*time.Minute).Unix()))

	// Steady state: upstream confirms 304 well before the first horizon.
	clock = clock.Add(15 * time.Minute)
	fc.ok = false
	qt.Assert(t, qt.IsNil(s.SyncType(ctx, trust.PIDProvider)))
	qt.Check(t, qt.Equals(fc.calls[1].etag, "snap-1")) // remembered ETag sent on the 304 poll

	raw, err = store.Get(ctx, key)
	qt.Assert(t, qt.IsNil(err))
	var second trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal(raw, &second)))
	qt.Check(t, qt.Equals(second.ValidUntil.Unix(), clock.Add(45*time.Minute).Unix())) // advanced past the first horizon
	qt.Check(t, qt.IsTrue(second.ValidUntil.After(first.ValidUntil)))
	qt.Check(t, qt.Equals(len(second.Anchors), 1)) // anchor content untouched by the 304
}

// End-to-end against the real store: a failed/unreachable poll must NEVER
// advance the embedded validUntil — only a confirmed 304 or 200 may (fail
// closed). Advancing on error would let a genuinely down trust-anchor service
// silently keep extending a eudi-verifier-core-visible deadline forever, defeating
// the whole point of the cache TTL.
func TestSyncTypeErrorDoesNotAdvanceEmbeddedValidUntilEndToEnd(t *testing.T) {
	mr := miniredis.RunT(t)
	store, err := valkeystore.New(valkeystore.Options{URL: mr.Addr()})
	qt.Assert(t, qt.IsNil(err))
	defer store.Close()

	clock := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	fc := &fakeClient{set: trust.AnchorSet{Anchors: []trust.Anchor{anchorLV(t)}, Snapshot: "snap-1", FetchedAt: clock}, ok: true}
	state := health.NewState([]string{trustcache.TypePIDProvider}, now)
	s := trustsync.New(fc, store, state, trustsync.Configuration{
		PollInterval: 15 * time.Minute, CacheTTL: 45 * time.Minute, Types: []string{trustcache.TypePIDProvider},
	}, zap.NewNop(), now)

	ctx := context.Background()
	key := trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV")

	qt.Assert(t, qt.IsNil(s.SyncType(ctx, trust.PIDProvider))) // 200

	raw, err := store.Get(ctx, key)
	qt.Assert(t, qt.IsNil(err))
	var before trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal(raw, &before)))

	// Trust-anchor service becomes unreachable, well before the first
	// horizon; time also advances, so any accidental "advance on poll" bug
	// (as opposed to only on confirmed 304/200) would be caught here too.
	clock = clock.Add(30 * time.Minute)
	fc.err = errors.New("trust-anchor unreachable")
	qt.Check(t, qt.IsNotNil(s.SyncType(ctx, trust.PIDProvider)))

	raw, err = store.Get(ctx, key)
	qt.Assert(t, qt.IsNil(err))
	var after trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal(raw, &after)))
	qt.Check(t, qt.Equals(after.ValidUntil.Unix(), before.ValidUntil.Unix())) // unchanged: fail closed
}
