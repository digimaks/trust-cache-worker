// Package trustsync implements the anchor sync loop over the go-eudi-trust
// client: per-type ETag polling against the trust service (polling + strong
// ETag; 304 = freshness confirmation; NO changes cursor) with atomic Valkey
// materialization. [ARF §6.6.3.6]: the snapshot is the authoritative anchor set.
package trustsync

import (
	"context"
	"fmt"
	"sync"
	"time"

	trust "github.com/gmb-eudi/go-eudi-trust"
	"github.com/gmb-lib/go-platform-kit/observability"
	"go.uber.org/zap"

	"github.com/digimaks/trust-cache-worker/internal/health"
	"github.com/digimaks/trust-cache-worker/internal/valkeystore"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

// Metric names (kit observability naming style; VictoriaMetrics registry).
const (
	MetricSyncTotal           = "trust_cache_sync_total"
	MetricSyncDurationSeconds = "trust_cache_sync_duration_seconds"
)

// Store is the write seam (implemented by valkeystore.Store; spied in tests).
type Store interface {
	SwapAnchorType(ctx context.Context, sw valkeystore.Swap) error
	RefreshAnchorType(ctx context.Context, keyType string, fr trustcache.TypeFreshness, ttl time.Duration) error
}

// Syncer polls one trust service and materializes anchors into Valkey.
type Syncer struct {
	client trust.Client
	store  Store
	state  *health.State
	cfg    Configuration
	log    *zap.Logger
	now    func() time.Time

	mu    sync.Mutex
	etags map[trust.AnchorType]string

	// runMu/inflight serialize whole sync cycles: the scheduled poll and an
	// operator resync share one run instead of racing per-type swaps.
	runMu    sync.Mutex
	inflight chan struct{}
}

// New builds a Syncer. nil clock = time.Now.
func New(client trust.Client, store Store, state *health.State, cfg Configuration, log *zap.Logger, now func() time.Time) *Syncer {
	if now == nil {
		now = time.Now
	}
	return &Syncer{
		client: client, store: store, state: state, cfg: cfg, log: log, now: now,
		etags: map[trust.AnchorType]string{},
	}
}

// Run executes one full sync cycle, or joins the cycle already in flight:
// a caller arriving while a cycle runs waits for that cycle to finish and
// returns with its result state, rather than starting a concurrent second
// cycle whose per-type swaps would interleave with the first's. The only
// error is the caller's own context expiring while waiting.
func (s *Syncer) Run(ctx context.Context) error {
	s.runMu.Lock()
	if done := s.inflight; done != nil {
		s.runMu.Unlock()
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan struct{})
	s.inflight = done
	s.runMu.Unlock()

	defer func() {
		s.runMu.Lock()
		s.inflight = nil
		s.runMu.Unlock()
		close(done)
	}()

	s.SyncAll(ctx)
	return nil
}

// SyncAll runs one poll cycle over every configured type. Per-type failures
// are logged and counted but do not abort the cycle: one broken type must not
// starve the others' freshness. Callers that can overlap (the scheduled task,
// an operator resync) go through Run instead, which serializes cycles.
func (s *Syncer) SyncAll(ctx context.Context) {
	types, err := ParseTypes(s.cfg.Types)
	if err != nil { // unreachable after config validation; belt-and-braces
		s.log.Error("invalid anchor type configuration", zap.Error(err))
		return
	}
	for _, t := range types {
		if err := s.SyncType(ctx, t); err != nil {
			s.log.Error("anchor sync failed", zap.String("anchor_type", string(t)), zap.Error(err))
		}
	}
}

// SyncType polls GET /v1/anchors.json?type=<t> with If-None-Match and
// materializes the result:
//   - 304 (ok=false): anchor CONTENT untouched, but every entry's embedded
//     validUntil advances to the same horizon as the refreshed TTL, and
//     freshness is refreshed — a 304 is upstream's freshness confirmation and
//     must advance the deadline exactly as a 200 does, or a healthy worker's
//     own cache can still trip a consumer's embedded-ValidUntil check;
//   - 200: atomic per-type swap — disappeared anchors/territories purge with
//     the swap;
//   - error: nothing written; the cache ages toward fail-closed expiry.
func (s *Syncer) SyncType(ctx context.Context, t trust.AnchorType) error {
	keyType, err := WireType(t)
	if err != nil {
		return err
	}

	start := s.now()
	set, ok, err := s.client.Anchors(ctx, t, "", s.etag(t))
	observability.ObserveSeconds(MetricSyncDurationSeconds,
		map[string]string{"type": keyType}, s.now().Sub(start).Seconds())
	if err != nil {
		observability.IncCounter(MetricSyncTotal,
			map[string]string{"type": keyType, observability.LabelOutcome: observability.OutcomeError})
		return fmt.Errorf("trustsync: anchors %s: %w", keyType, err)
	}

	now := s.now()
	validUntil := now.Add(s.cfg.CacheTTL)

	if !ok { // 304 Not Modified — cache still fresh upstream
		fr := trustcache.TypeFreshness{SnapshotID: s.etag(t), FetchedAt: now, ValidUntil: validUntil}
		if err := s.store.RefreshAnchorType(ctx, keyType, fr, s.cfg.CacheTTL); err != nil {
			observability.IncCounter(MetricSyncTotal,
				map[string]string{"type": keyType, observability.LabelOutcome: observability.OutcomeError})
			return err
		}
		s.state.SetTypeFreshness(keyType, fr.SnapshotID, fr.FetchedAt, fr.ValidUntil, fr.UpstreamStale)
		observability.IncCounter(MetricSyncTotal, map[string]string{"type": keyType, "outcome": "not_modified"})
		return nil
	}

	fr := trustcache.TypeFreshness{SnapshotID: set.Snapshot, FetchedAt: now, ValidUntil: validUntil, UpstreamStale: set.Stale}
	sw := valkeystore.Swap{
		KeyType:   keyType,
		ETag:      set.Snapshot,
		Entries:   entriesByTerritory(keyType, set, now, validUntil),
		Freshness: fr,
		TTL:       s.cfg.CacheTTL,
	}
	if err := s.store.SwapAnchorType(ctx, sw); err != nil {
		observability.IncCounter(MetricSyncTotal,
			map[string]string{"type": keyType, observability.LabelOutcome: observability.OutcomeError})
		return err
	}
	s.setEtag(t, set.Snapshot)
	s.state.SetTypeFreshness(keyType, fr.SnapshotID, fr.FetchedAt, fr.ValidUntil, fr.UpstreamStale)
	observability.IncCounter(MetricSyncTotal, map[string]string{"type": keyType, "outcome": "updated"})
	s.log.Info("anchor set materialized",
		zap.String("anchor_type", keyType),
		zap.String("snapshot", set.Snapshot),
		zap.Int("territories", len(sw.Entries)),
		zap.Bool("upstream_stale", set.Stale))
	return nil
}

// entriesByTerritory groups the flat anchor list into per-territory cache
// entries (the key granularity eudi-verifier-core reads at: AnchorsFor(type,
// country)).
func entriesByTerritory(keyType string, set trust.AnchorSet, fetchedAt, validUntil time.Time) []trustcache.AnchorSetEntry {
	byTerritory := map[string][]trustcache.Anchor{}
	for _, a := range set.Anchors {
		territory := trustcache.NormalizeTerritory(a.Country)
		var der []byte
		if a.Cert != nil {
			der = a.Cert.Raw
		}
		byTerritory[territory] = append(byTerritory[territory], trustcache.Anchor{
			CertDER:    der,
			Territory:  territory,
			Status:     a.Status,
			ValidUntil: a.ValidUntil,
			TLSequence: a.TLSequence,
			UseCases:   a.UseCases, // preserve accredited use-case scoping across the cache round-trip
		})
	}
	out := make([]trustcache.AnchorSetEntry, 0, len(byTerritory))
	for territory, anchors := range byTerritory {
		out = append(out, trustcache.AnchorSetEntry{
			SnapshotID:    set.Snapshot,
			Type:          keyType,
			Territory:     territory,
			FetchedAt:     fetchedAt,
			ValidUntil:    validUntil,
			UpstreamStale: set.Stale,
			Anchors:       anchors,
		})
	}
	return out
}

func (s *Syncer) etag(t trust.AnchorType) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.etags[t]
}

func (s *Syncer) setEtag(t trust.AnchorType, etag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.etags[t] = etag
}
