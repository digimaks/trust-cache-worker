// Package telemetry polls GET /v1/snapshot for LOTL sequence, per-territory
// staleness, the upstream stale flag and the pending-bootstrap state
// (trust.bootstrap_review_needed — the LOTL signer bootstrap is MANUAL
// upstream and never auto-activates), and publishes it to ops (gauges + Warn)
// and to eudi-verifier-core (trust:freshness:snapshot).
package telemetry

import (
	"context"
	"fmt"
	"time"

	trust "github.com/gmb-eudi/go-eudi-trust"
	"github.com/gmb-lib/go-platform-kit/observability"
	"go.uber.org/zap"

	"github.com/dativa-lv/trust-cache-worker/internal/health"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

// MetricSnapshotPollTotal counts snapshot polls by outcome.
const MetricSnapshotPollTotal = "trust_cache_snapshot_poll_total"

// SnapshotStore is the write seam (valkeystore.Store implements it).
type SnapshotStore interface {
	SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error
}

// Poller publishes snapshot freshness telemetry.
type Poller struct {
	client   trust.Client
	store    SnapshotStore
	state    *health.State
	freshFor time.Duration // Valkey TTL of the health key (3 x poll interval)
	log      *zap.Logger
	now      func() time.Time
}

// New builds a Poller. nil clock = time.Now.
func New(client trust.Client, store SnapshotStore, state *health.State, freshFor time.Duration, log *zap.Logger, now func() time.Time) *Poller {
	if now == nil {
		now = time.Now
	}
	return &Poller{client: client, store: store, state: state, freshFor: freshFor, log: log, now: now}
}

// Poll fetches /v1/snapshot and publishes SnapshotHealth. On failure nothing
// is written: the previously written key ages toward its TTL and consumers
// see the degradation (fail-closed posture).
func (p *Poller) Poll(ctx context.Context) error {
	meta, err := p.client.Snapshot(ctx)
	if err != nil {
		observability.IncCounter(MetricSnapshotPollTotal,
			map[string]string{observability.LabelOutcome: observability.OutcomeError})
		return fmt.Errorf("telemetry: snapshot: %w", err)
	}

	// The trust client's SnapshotMeta type has no top-level Stale field —
	// staleness is per-territory only. The aggregate upstream-stale signal is
	// the OR of every territory's Stale flag: any stale territory means the
	// snapshot as a whole is degraded (conservative — surfaces degradation
	// rather than hiding it behind a missing signal).
	upstreamStale := false
	for _, t := range meta.Territories {
		if t.Stale {
			upstreamStale = true
			break
		}
	}

	h := trustcache.SnapshotHealth{
		SnapshotID:       meta.ID,
		LOTLSequence:     meta.LOTLSequence,
		CheckedAt:        p.now(),
		UpstreamStale:    upstreamStale,
		PendingBootstrap: meta.PendingBootstrap,
		Territories:      make(map[string]trustcache.TerritoryHealth, len(meta.Territories)),
	}
	for _, t := range meta.Territories {
		h.Territories[trustcache.NormalizeTerritory(t.Code)] = trustcache.TerritoryHealth{
			TLSequence: safeUint64(t.TLSequence),
			Stale:      t.Stale,
			NextUpdate: t.NextUpdate,
		}
	}

	if err := p.store.SetJSON(ctx, trustcache.SnapshotHealthKey, h, p.freshFor); err != nil {
		observability.IncCounter(MetricSnapshotPollTotal,
			map[string]string{observability.LabelOutcome: observability.OutcomeError})
		return fmt.Errorf("telemetry: publish snapshot health: %w", err)
	}
	p.state.SetSnapshot(h)

	if h.PendingBootstrap {
		// The pending-bootstrap ALERT: upstream staged a new LOTL signer set
		// awaiting manual operator approval — it never auto-activates. Surfaced
		// as trust_cache_pending_bootstrap=1 + this Warn for alert routing.
		p.log.Warn("upstream trust service has a pending LOTL bootstrap awaiting operator approval (trust.bootstrap_review_needed)",
			zap.String("snapshot", meta.ID))
	}
	if h.UpstreamStale {
		p.log.Warn("trust service reports stale trust data (X-Trust-Stale)",
			zap.String("snapshot", meta.ID))
	}

	observability.IncCounter(MetricSnapshotPollTotal,
		map[string]string{observability.LabelOutcome: observability.OutcomeSuccess})
	return nil
}

// safeUint64 clamps a negative sequence number to 0 instead of letting it
// wrap to a huge uint64. trust.TerritoryStatus.TLSequence comes from the trust
// client's untrusted upstream JSON, which is decoded with no non-negative
// check, so a malformed value must fail closed to "unknown" (0) rather than
// silently becoming an implausibly large sequence number.
func safeUint64(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}
