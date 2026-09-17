package health

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/VictoriaMetrics/metrics"
)

// Gauge names for freshness telemetry. Kit-style naming; the kit's
// observability helpers only wrap counters/histograms, so gauges register on
// the same VictoriaMetrics default registry Azugo serves at /metrics.
const (
	MetricAnchorStalenessSeconds = "trust_cache_anchor_staleness_seconds"
	MetricLOTLSequence           = "trust_cache_lotl_sequence"
	MetricUpstreamStale          = "trust_cache_upstream_stale"
	MetricPendingBootstrap       = "trust_cache_pending_bootstrap"
	MetricTerritoryStale         = "trust_cache_territory_stale"
)

// current is the live State the gauge callbacks read through. Gauges register
// once per process (VictoriaMetrics keeps the first callback), so callbacks
// must not capture a State instance — tests create many.
var current atomic.Pointer[State]

var registerFixedOnce sync.Once

// registeredLabels guards per-label gauge registration (metric name -> set).
var registeredLabels sync.Map

// RegisterMetrics publishes this State as the metrics source and registers
// the fixed-name gauges.
func (s *State) RegisterMetrics() {
	current.Store(s)
	registerFixedOnce.Do(func() {
		metrics.GetOrCreateGauge(MetricLOTLSequence, func() float64 {
			if st := current.Load(); st != nil {
				return st.lotlSequence()
			}
			return 0
		})
		metrics.GetOrCreateGauge(MetricUpstreamStale, func() float64 {
			if st := current.Load(); st != nil {
				return st.upstreamStale()
			}
			return 0
		})
		metrics.GetOrCreateGauge(MetricPendingBootstrap, func() float64 {
			if st := current.Load(); st != nil {
				return st.pendingBootstrap()
			}
			return 0
		})
	})
}

func registerTypeGauge(keyType string) {
	name := fmt.Sprintf(`%s{type=%q}`, MetricAnchorStalenessSeconds, keyType)
	if _, loaded := registeredLabels.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	metrics.GetOrCreateGauge(name, func() float64 {
		if st := current.Load(); st != nil {
			return st.typeStalenessSeconds(keyType)
		}
		return -1
	})
}

func registerTerritoryGauge(code string) {
	name := fmt.Sprintf(`%s{territory=%q}`, MetricTerritoryStale, code)
	if _, loaded := registeredLabels.LoadOrStore(name, struct{}{}); loaded {
		return
	}
	metrics.GetOrCreateGauge(name, func() float64 {
		if st := current.Load(); st != nil {
			return st.territoryStale(code)
		}
		return 0
	})
}
