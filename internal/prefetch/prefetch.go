// Package prefetch refreshes the top-N recently referenced status lists ahead
// of their TTL so the wallet-facing verifier's revocation checks
// ([ARF §6.6.3.7]) hit a warm cache. Refs come from the shared Valkey ZSET
// (trustcache.StatusRefsKey) that the verifier writes — the StatusRefSource
// interface is defined here NOW, ready to be wired to that traffic.
package prefetch

import (
	"context"
	"fmt"
	"time"

	"github.com/gmb-lib/go-platform-kit/observability"
	"go.uber.org/zap"

	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

// MetricPrefetchTotal counts prefetch decisions; hit ratio =
// hit / (hit + refreshed) — the cache-hit ratio metric.
const MetricPrefetchTotal = "trust_cache_statuslist_prefetch_total"

// refsKeepBound bounds the shared refs ZSET (trimmed every cycle).
const refsKeepBound = 1000

// StatusRefSource lists recently referenced status-list URIs, most recent
// first. Production: valkeystore.Store.TopStatusRefs over the ZSET written by
// the wallet-facing verifier.
type StatusRefSource interface {
	TopStatusRefs(ctx context.Context, n int) ([]string, error)
}

// StatusStore is the cache side (implemented by valkeystore.Store).
type StatusStore interface {
	GetTTL(ctx context.Context, key string) (time.Duration, bool, error)
	SetBytes(ctx context.Context, key string, val []byte, ttl time.Duration) error
	TrimStatusRefs(ctx context.Context, keep int) error
}

// Prefetcher runs one prefetch pass per schedule tick.
type Prefetcher struct {
	refs  StatusRefSource
	fetch Fetcher
	store StatusStore
	cfg   Configuration
	log   *zap.Logger
	now   func() time.Time
}

// New builds a Prefetcher. nil clock = time.Now.
func New(refs StatusRefSource, fetch Fetcher, store StatusStore, cfg Configuration, log *zap.Logger, now func() time.Time) *Prefetcher {
	if now == nil {
		now = time.Now
	}
	return &Prefetcher{refs: refs, fetch: fetch, store: store, cfg: cfg, log: log, now: now}
}

// Run refreshes every top-N URI whose cache entry will not survive until the
// next cycle. Per-URI failures are counted + logged and never abort the pass.
func (p *Prefetcher) Run(ctx context.Context) error {
	if p.cfg.TopN <= 0 {
		return nil
	}
	uris, err := p.refs.TopStatusRefs(ctx, p.cfg.TopN)
	if err != nil {
		return fmt.Errorf("prefetch: list refs: %w", err)
	}
	for _, uri := range uris {
		key := trustcache.StatusListKey(uri)
		ttl, exists, err := p.store.GetTTL(ctx, key)
		if err != nil {
			observability.IncCounter(MetricPrefetchTotal,
				map[string]string{observability.LabelOutcome: observability.OutcomeError})
			p.log.Warn("status list ttl check failed", zap.String("list_key", key), zap.Error(err))
			continue
		}
		if exists && ttl > p.cfg.Interval {
			observability.IncCounter(MetricPrefetchTotal, map[string]string{observability.LabelOutcome: "hit"})
			continue
		}
		body, err := p.fetch.Get(ctx, uri)
		if err != nil {
			observability.IncCounter(MetricPrefetchTotal,
				map[string]string{observability.LabelOutcome: observability.OutcomeError})
			// Hashed key, not the raw URI — issuer URIs must stay out of logs.
			p.log.Warn("status list prefetch failed", zap.String("list_key", key), zap.Error(err))
			continue
		}
		cacheTTL := clampTTL(PeekTTL(body, p.cfg.DefaultTTL, p.now()), p.cfg.MinTTL, p.cfg.MaxTTL)
		if err := p.store.SetBytes(ctx, key, body, cacheTTL); err != nil {
			observability.IncCounter(MetricPrefetchTotal,
				map[string]string{observability.LabelOutcome: observability.OutcomeError})
			p.log.Warn("status list cache write failed", zap.String("list_key", key), zap.Error(err))
			continue
		}
		observability.IncCounter(MetricPrefetchTotal, map[string]string{observability.LabelOutcome: "refreshed"})
	}
	if err := p.store.TrimStatusRefs(ctx, refsKeepBound); err != nil {
		p.log.Warn("status refs trim failed", zap.Error(err))
	}
	return nil
}

func clampTTL(ttl, lo, hi time.Duration) time.Duration {
	if ttl < lo {
		return lo
	}
	if ttl > hi {
		return hi
	}
	return ttl
}
