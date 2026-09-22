package valkeystore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-quicktest/qt"

	"github.com/digimaks/trust-cache-worker/internal/valkeystore"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

func testStore(t *testing.T) (*valkeystore.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	s, err := valkeystore.New(valkeystore.Options{URL: mr.Addr()})
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(s.Close)
	return s, mr
}

func zadd(t *testing.T, mr *miniredis.Miniredis, score float64, member string) {
	t.Helper()
	_, err := mr.ZAdd(trustcache.StatusRefsKey, score, member)
	qt.Assert(t, qt.IsNil(err))
}

func entry(territory string, snapshotID string, ttl time.Duration, now time.Time, anchors ...trustcache.Anchor) trustcache.AnchorSetEntry {
	return trustcache.AnchorSetEntry{
		SnapshotID: snapshotID,
		Type:       trustcache.TypePIDProvider,
		Territory:  territory,
		FetchedAt:  now,
		ValidUntil: now.Add(ttl),
		Anchors:    anchors,
	}
}

var (
	anchorA = trustcache.Anchor{CertDER: []byte{0x0A}, Territory: "LV", Status: "granted", TLSequence: 51}
	anchorB = trustcache.Anchor{CertDER: []byte{0x0B}, Territory: "LV", Status: "granted", TLSequence: 51}
	anchorC = trustcache.Anchor{CertDER: []byte{0x0C}, Territory: "EE", Status: "granted", TLSequence: 73}
)

func TestSwapWritesAllKeysWithTTL(t *testing.T) {
	s, mr := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ttl := 45 * time.Minute

	sw := valkeystore.Swap{
		KeyType: trustcache.TypePIDProvider,
		ETag:    "snap-1",
		Entries: []trustcache.AnchorSetEntry{
			entry("LV", "snap-1", ttl, now, anchorA, anchorB),
			entry("EE", "snap-1", ttl, now, anchorC),
		},
		Freshness: trustcache.TypeFreshness{SnapshotID: "snap-1", FetchedAt: now, ValidUntil: now.Add(ttl)},
		TTL:       ttl,
	}
	qt.Assert(t, qt.IsNil(s.SwapAnchorType(ctx, sw)))

	for _, key := range []string{
		trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV"),
		trustcache.AnchorSetKey(trustcache.TypePIDProvider, "EE"),
		trustcache.TerritoryIndexKey(trustcache.TypePIDProvider),
		trustcache.ETagKey(trustcache.TypePIDProvider),
		trustcache.FreshnessKey(trustcache.TypePIDProvider),
	} {
		qt.Check(t, qt.IsTrue(mr.Exists(key)), qt.Commentf("key %s", key))
		qt.Check(t, qt.IsTrue(mr.TTL(key) > 0), qt.Commentf("key %s must carry a TTL — fail closed", key))
	}

	raw, err := s.Get(ctx, trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV"))
	qt.Assert(t, qt.IsNil(err))
	var got trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal(raw, &got)))
	qt.Check(t, qt.Equals(len(got.Anchors), 2))

	var idx []string
	rawIdx, _ := s.Get(ctx, trustcache.TerritoryIndexKey(trustcache.TypePIDProvider))
	qt.Assert(t, qt.IsNil(json.Unmarshal(rawIdx, &idx)))
	qt.Check(t, qt.DeepEquals(idx, []string{"EE", "LV"})) // sorted, deterministic
}

// Acceptance: an anchor (or whole territory) absent from the new
// snapshot is gone from Valkey immediately after the swap — well inside the
// required 5 s (the swap is synchronous within the poll; a reader sees
// strictly before-swap or strictly after-swap state thanks to MULTI/EXEC).
func TestSwapPurgesDisappearedAnchorsAndTerritories(t *testing.T) {
	s, mr := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ttl := 45 * time.Minute

	// Snapshot v1: LV{A,B} + EE{C}.
	qt.Assert(t, qt.IsNil(s.SwapAnchorType(ctx, valkeystore.Swap{
		KeyType: trustcache.TypePIDProvider, ETag: "snap-1",
		Entries: []trustcache.AnchorSetEntry{
			entry("LV", "snap-1", ttl, now, anchorA, anchorB),
			entry("EE", "snap-1", ttl, now, anchorC),
		},
		Freshness: trustcache.TypeFreshness{SnapshotID: "snap-1", FetchedAt: now, ValidUntil: now.Add(ttl)},
		TTL:       ttl,
	})))

	// Snapshot v2: anchor A withdrawn, territory EE gone entirely.
	start := time.Now()
	qt.Assert(t, qt.IsNil(s.SwapAnchorType(ctx, valkeystore.Swap{
		KeyType: trustcache.TypePIDProvider, ETag: "snap-2",
		Entries: []trustcache.AnchorSetEntry{
			entry("LV", "snap-2", ttl, now, anchorB),
		},
		Freshness: trustcache.TypeFreshness{SnapshotID: "snap-2", FetchedAt: now, ValidUntil: now.Add(ttl)},
		TTL:       ttl,
	})))
	qt.Check(t, qt.IsTrue(time.Since(start) < 5*time.Second)) // gone < 5 s after poll

	// Withdrawn anchor A purged with the swap (snapshot is authoritative).
	raw, err := s.Get(ctx, trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV"))
	qt.Assert(t, qt.IsNil(err))
	var got trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal(raw, &got)))
	qt.Assert(t, qt.Equals(len(got.Anchors), 1))
	qt.Check(t, qt.DeepEquals(got.Anchors[0].CertDER, []byte{0x0B}))

	// Vanished territory key DELed in the same EXEC.
	qt.Check(t, qt.IsFalse(mr.Exists(trustcache.AnchorSetKey(trustcache.TypePIDProvider, "EE"))))

	var idx []string
	rawIdx, _ := s.Get(ctx, trustcache.TerritoryIndexKey(trustcache.TypePIDProvider))
	qt.Assert(t, qt.IsNil(json.Unmarshal(rawIdx, &idx)))
	qt.Check(t, qt.DeepEquals(idx, []string{"LV"}))
}

// The 304 path: anchor CONTENT is untouched (no DEL, no change to the
// anchors list, snapshot id, territory, or fetch provenance), but the
// embedded validUntil ADVANCES to the same horizon as the refreshed TTL. A
// 304 is upstream's explicit confirmation the snapshot is still current, so
// the cache-validity deadline must advance exactly as SwapAnchorType's does
// on a 200 (trustcache/keys.go: "TTL = the entry's ValidUntil horizon" — the
// two are meant to always name the same instant).
//
// Before this fix, RefreshAnchorType only EXPIRE'd the raw key and left the
// embedded validUntil frozen at the last 200 — the wallet-facing verifier's
// AnchorsFor (v.now().After(entry.ValidUntil)) fails closed the instant that frozen
// deadline passes, even though the Valkey key itself is still alive and
// successfully refreshing every cycle (the steady-state fail-closed trap,
// confirmed empirically on the live stack: a deployment whose snapshot
// stays unchanged longer than CacheTTL bricks itself until the worker
// restarts).
func TestRefreshAdvancesEmbeddedValidUntilButNotAnchorContent(t *testing.T) {
	s, mr := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ttl := 45 * time.Minute

	qt.Assert(t, qt.IsNil(s.SwapAnchorType(ctx, valkeystore.Swap{
		KeyType: trustcache.TypePIDProvider, ETag: "snap-1",
		Entries:   []trustcache.AnchorSetEntry{entry("LV", "snap-1", ttl, now, anchorA)},
		Freshness: trustcache.TypeFreshness{SnapshotID: "snap-1", FetchedAt: now, ValidUntil: now.Add(ttl)},
		TTL:       ttl,
	})))

	key := trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV")
	beforeRaw, err := s.Get(ctx, key)
	qt.Assert(t, qt.IsNil(err))
	var before trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal(beforeRaw, &before)))

	// Let 30m of TTL burn down, then a 304 cycle refreshes.
	mr.FastForward(30 * time.Minute)
	qt.Check(t, qt.IsTrue(mr.TTL(key) <= 15*time.Minute))

	later := now.Add(30 * time.Minute)
	newValidUntil := later.Add(ttl)
	qt.Assert(t, qt.IsNil(s.RefreshAnchorType(ctx, trustcache.TypePIDProvider,
		trustcache.TypeFreshness{SnapshotID: "snap-1", FetchedAt: later, ValidUntil: newValidUntil}, ttl)))

	afterRaw, err := s.Get(ctx, key)
	qt.Assert(t, qt.IsNil(err))
	var after trustcache.AnchorSetEntry
	qt.Assert(t, qt.IsNil(json.Unmarshal(afterRaw, &after)))

	// The embedded deadline advanced to the SAME horizon as the refreshed TTL...
	qt.Check(t, qt.Equals(after.ValidUntil.Unix(), newValidUntil.Unix()))
	qt.Check(t, qt.IsTrue(mr.TTL(key) > 40*time.Minute), qt.Commentf("TTL must be extended"))

	// ...but nothing else about the entry churned: same anchors, same
	// snapshot/type/territory, same fetch provenance (FetchedAt is honest
	// history of when the DATA was last actually fetched — a 304 re-stamps
	// only the cache-validity horizon, not provenance).
	qt.Check(t, qt.Equals(after.SnapshotID, before.SnapshotID))
	qt.Check(t, qt.Equals(after.Type, before.Type))
	qt.Check(t, qt.Equals(after.Territory, before.Territory))
	qt.Check(t, qt.Equals(after.FetchedAt.Unix(), before.FetchedAt.Unix()))
	qt.Check(t, qt.DeepEquals(after.Anchors, before.Anchors))

	var fr trustcache.TypeFreshness
	rawFr, _ := s.Get(ctx, trustcache.FreshnessKey(trustcache.TypePIDProvider))
	qt.Assert(t, qt.IsNil(json.Unmarshal(rawFr, &fr)))
	qt.Check(t, qt.Equals(fr.FetchedAt.Unix(), later.Unix()))
}

// A vanished anchor-set key (already TTL-expired between the territory-index
// read and the refresh — a rare race, not the steady-state case) must not
// fail the whole refresh: RefreshAnchorType falls back to EXPIRE for that one
// key (a no-op on an absent key) and still commits the rest atomically.
func TestRefreshToleratesVanishedAnchorSetKey(t *testing.T) {
	s, mr := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ttl := 45 * time.Minute

	qt.Assert(t, qt.IsNil(s.SwapAnchorType(ctx, valkeystore.Swap{
		KeyType: trustcache.TypePIDProvider, ETag: "snap-1",
		Entries:   []trustcache.AnchorSetEntry{entry("LV", "snap-1", ttl, now, anchorA)},
		Freshness: trustcache.TypeFreshness{SnapshotID: "snap-1", FetchedAt: now, ValidUntil: now.Add(ttl)},
		TTL:       ttl,
	})))

	key := trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV")
	qt.Assert(t, qt.IsTrue(mr.Del(key))) // simulate the key vanishing out from under the refresh

	later := now.Add(30 * time.Minute)
	err := s.RefreshAnchorType(ctx, trustcache.TypePIDProvider,
		trustcache.TypeFreshness{SnapshotID: "snap-1", FetchedAt: later, ValidUntil: later.Add(ttl)}, ttl)
	qt.Assert(t, qt.IsNil(err))

	qt.Check(t, qt.IsFalse(mr.Exists(key))) // still gone — EXPIRE on an absent key is a no-op, not a re-create

	// The territory index / etag / freshness keys the transaction DID own
	// still committed.
	qt.Check(t, qt.IsTrue(mr.Exists(trustcache.FreshnessKey(trustcache.TypePIDProvider))))
}

func TestGetMissingKeyReturnsNilNil(t *testing.T) {
	s, _ := testStore(t)
	raw, err := s.Get(context.Background(), "trust:anchors:pid_provider:XX")
	qt.Check(t, qt.IsNil(err))
	qt.Check(t, qt.IsNil(raw))
}

func TestGetTTL(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	qt.Assert(t, qt.IsNil(s.SetBytes(ctx, "trust:statuslist:abc", []byte("tok"), 5*time.Minute)))

	ttl, exists, err := s.GetTTL(ctx, "trust:statuslist:abc")
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(exists))
	qt.Check(t, qt.IsTrue(ttl > 4*time.Minute && ttl <= 5*time.Minute))

	_, exists, err = s.GetTTL(ctx, "trust:statuslist:missing")
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsFalse(exists))
}

func TestTopStatusRefsMostRecentFirst(t *testing.T) {
	s, mr := testStore(t)
	ctx := context.Background()
	zadd(t, mr, 100, "https://a.example/1")
	zadd(t, mr, 300, "https://c.example/3")
	zadd(t, mr, 200, "https://b.example/2")

	got, err := s.TopStatusRefs(ctx, 2)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(got, []string{"https://c.example/3", "https://b.example/2"}))

	all, err := s.TopStatusRefs(ctx, 50)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(len(all), 3))
}

// TrimStatusRefs boundary — the ZSET has FEWER members than `keep`.
// ZREMRANGEBYRANK key 0 (-keep-1) must remove NOTHING here: with 3 members and
// keep=50 the stop rank is -51, which real Redis/Valkey (and miniredis) resolve
// to an empty range (start 0 > resolved end) — a "trim to N" must never trim
// when there is nothing above N. Regression guard: if the stop formula were
// wrong this would wipe live status refs (a fail-closed hazard for the
// prefetch — the top-N would be emptied).
func TestTrimStatusRefsFewerThanKeepRemovesNothing(t *testing.T) {
	s, mr := testStore(t)
	ctx := context.Background()
	zadd(t, mr, 100, "https://a.example/1")
	zadd(t, mr, 200, "https://b.example/2")
	zadd(t, mr, 300, "https://c.example/3")

	qt.Assert(t, qt.IsNil(s.TrimStatusRefs(ctx, 50)))

	members, err := mr.ZMembers(trustcache.StatusRefsKey)
	qt.Assert(t, qt.IsNil(err))
	// All three survive (ascending by score).
	qt.Check(t, qt.DeepEquals(members, []string{
		"https://a.example/1", "https://b.example/2", "https://c.example/3",
	}))

	// Exact boundary: members == keep must also remove nothing.
	qt.Assert(t, qt.IsNil(s.TrimStatusRefs(ctx, 3)))
	members, err = mr.ZMembers(trustcache.StatusRefsKey)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(len(members), 3))
}

// TrimStatusRefs bounds the set to the newest `keep`: with 5 members and keep=2
// the three oldest (lowest score) are dropped and the two newest survive.
func TestTrimStatusRefsMoreThanKeepRemovesOldest(t *testing.T) {
	s, mr := testStore(t)
	ctx := context.Background()
	zadd(t, mr, 100, "https://a.example/1")
	zadd(t, mr, 200, "https://b.example/2")
	zadd(t, mr, 300, "https://c.example/3")
	zadd(t, mr, 400, "https://d.example/4")
	zadd(t, mr, 500, "https://e.example/5")

	qt.Assert(t, qt.IsNil(s.TrimStatusRefs(ctx, 2)))

	members, err := mr.ZMembers(trustcache.StatusRefsKey)
	qt.Assert(t, qt.IsNil(err))
	// Newest two survive (ascending by score).
	qt.Check(t, qt.DeepEquals(members, []string{"https://d.example/4", "https://e.example/5"}))

	// And they are still the ones TopStatusRefs would return, newest first.
	top, err := s.TopStatusRefs(ctx, 10)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(top, []string{"https://e.example/5", "https://d.example/4"}))
}
