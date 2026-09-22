package health_test

import (
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/digimaks/trust-cache-worker/internal/health"
)

func TestReadyLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	tests := []struct {
		name      string
		required  []string
		mutate    func(s *health.State)
		wantReady bool
	}{
		{"no_types_configured_not_ready", nil, func(*health.State) {}, false},
		{"never_synced_not_ready", []string{"pid_provider"}, func(*health.State) {}, false},
		{
			"synced_fresh_ready", []string{"pid_provider"},
			func(s *health.State) {
				s.SetTypeFreshness("pid_provider", "snap-1", now, now.Add(45*time.Minute), false)
			},
			true,
		},
		{
			"one_of_two_missing_not_ready", []string{"pid_provider", "access_ca"},
			func(s *health.State) {
				s.SetTypeFreshness("pid_provider", "snap-1", now, now.Add(45*time.Minute), false)
			},
			false,
		},
		{
			"expired_not_ready_fail_closed", []string{"pid_provider"},
			func(s *health.State) {
				s.SetTypeFreshness("pid_provider", "snap-1", now.Add(-2*time.Hour), now.Add(-time.Minute), false)
			},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := health.NewState(tt.required, clock)
			tt.mutate(s)
			ready, reason := s.Ready()
			qt.Check(t, qt.Equals(ready, tt.wantReady))
			if !tt.wantReady {
				qt.Check(t, qt.Not(qt.Equals(reason, "")))
			}
		})
	}
}

// TypeStatuses reports every configured type in configuration order, and a
// never-synced type appears with Synced=false and zero identity — absence is
// exactly what an operator diagnosing propagation needs to see.
func TestTypeStatusesReportsConfiguredOrderIncludingNeverSynced(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	s := health.NewState([]string{"pid_provider", "qeaa_provider"}, func() time.Time { return now })

	s.SetTypeFreshness("qeaa_provider", "snap-7", now, now.Add(time.Hour), true)

	got := s.TypeStatuses()
	qt.Assert(t, qt.HasLen(got, 2))

	qt.Check(t, qt.Equals(got[0].Type, "pid_provider"))
	qt.Check(t, qt.IsFalse(got[0].Synced))
	qt.Check(t, qt.Equals(got[0].SnapshotID, ""))

	qt.Check(t, qt.Equals(got[1].Type, "qeaa_provider"))
	qt.Check(t, qt.IsTrue(got[1].Synced))
	qt.Check(t, qt.Equals(got[1].SnapshotID, "snap-7"))
	qt.Check(t, qt.Equals(got[1].FetchedAt, now))
	qt.Check(t, qt.Equals(got[1].ValidUntil, now.Add(time.Hour)))
	qt.Check(t, qt.IsTrue(got[1].UpstreamStale))
}
