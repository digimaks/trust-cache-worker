package trustsync_test

import (
	"context"
	"testing"
	"time"

	trust "github.com/gmb-eudi/go-eudi-trust"
	"github.com/go-quicktest/qt"
	"go.uber.org/zap"

	"github.com/digimaks/trust-cache-worker/internal/health"
	"github.com/digimaks/trust-cache-worker/internal/trustsync"
)

// oneTypeCfg keeps the join tests single-type so a cycle is exactly one
// client call and the call counting stays readable.
func oneTypeCfg() trustsync.Configuration {
	return trustsync.Configuration{
		PollInterval: time.Minute, CacheTTL: time.Hour,
		SnapshotPollInterval: time.Minute, Types: []string{"pid_provider"},
	}
}

// A second Run arriving while a cycle is in flight must JOIN that cycle —
// wait for it and return — not start a concurrent second one whose per-type
// swaps would interleave with the first's. The proof is the client call
// count: one cycle's worth, not two.
func TestRunJoinsInFlightCycle(t *testing.T) {
	gate := make(chan struct{})
	fc := &fakeClient{
		set:  trust.AnchorSet{Anchors: nil, Snapshot: "snap-1", FetchedAt: time.Now()},
		ok:   true,
		gate: gate,
	}
	st := &spyStore{}
	s := trustsync.New(fc, st, health.NewState([]string{"pid_provider"}, nil), oneTypeCfg(), zap.NewNop(), nil)

	first := make(chan error, 1)
	go func() { first <- s.Run(context.Background()) }()

	// Wait until cycle #1 is provably in flight (blocked inside the client).
	deadline := time.After(5 * time.Second)
	for fc.callCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("first cycle never reached the client")
		case <-time.After(5 * time.Millisecond):
		}
	}

	second := make(chan error, 1)
	go func() { second <- s.Run(context.Background()) }()

	// The joiner must not have started its own cycle while #1 is in flight.
	time.Sleep(50 * time.Millisecond)
	qt.Assert(t, qt.Equals(fc.callCount(), 1))

	close(gate) // release cycle #1; both Runs must now return
	qt.Assert(t, qt.IsNil(<-first))
	qt.Assert(t, qt.IsNil(<-second))

	// Still one cycle's worth of client calls: the joiner really joined.
	qt.Assert(t, qt.Equals(fc.callCount(), 1))
}

// A joiner whose own context expires while waiting gets that context error
// back promptly instead of waiting out the in-flight cycle.
func TestRunJoinerContextExpiry(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	fc := &fakeClient{
		set:  trust.AnchorSet{Anchors: nil, Snapshot: "snap-1", FetchedAt: time.Now()},
		ok:   true,
		gate: gate,
	}
	s := trustsync.New(fc, &spyStore{}, health.NewState([]string{"pid_provider"}, nil), oneTypeCfg(), zap.NewNop(), nil)

	go func() { _ = s.Run(context.Background()) }()
	deadline := time.After(5 * time.Second)
	for fc.callCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("first cycle never reached the client")
		case <-time.After(5 * time.Millisecond):
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.Run(ctx)
	qt.Assert(t, qt.ErrorIs(err, context.Canceled))
	// And the joiner's exit did not count as a cycle of its own.
	qt.Assert(t, qt.Equals(fc.callCount(), 1))
}
