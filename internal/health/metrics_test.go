package health_test

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/go-quicktest/qt"

	"github.com/digimaks/trust-cache-worker/internal/health"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

func TestGaugesReflectState(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	s := health.NewState([]string{trustcache.TypePIDProvider}, func() time.Time { return now })
	s.RegisterMetrics()

	s.SetTypeFreshness(trustcache.TypePIDProvider, "snap-1", now.Add(-90*time.Second), now.Add(45*time.Minute), false)
	s.SetSnapshot(trustcache.SnapshotHealth{
		SnapshotID:       "snap-1",
		LOTLSequence:     388,
		CheckedAt:        now,
		UpstreamStale:    true,
		PendingBootstrap: true,
		Territories:      map[string]trustcache.TerritoryHealth{"LV": {TLSequence: 51, Stale: true}, "EE": {TLSequence: 73}},
	})

	var buf bytes.Buffer
	metrics.WritePrometheus(&buf, false)
	out := buf.String()

	for _, want := range []string{
		`trust_cache_anchor_staleness_seconds{type="pid_provider"} 90`,
		`trust_cache_lotl_sequence 388`,
		`trust_cache_upstream_stale 1`,
		`trust_cache_pending_bootstrap 1`, // pending-bootstrap alert visible to ops
		`trust_cache_territory_stale{territory="LV"} 1`,
		`trust_cache_territory_stale{territory="EE"} 0`,
	} {
		qt.Check(t, qt.IsTrue(strings.Contains(out, want)), qt.Commentf("missing %q in scrape:\n%s", want, out))
	}
}
