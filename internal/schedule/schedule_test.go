package schedule_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gmb-lib/go-platform-kit/propagation"
	"github.com/go-quicktest/qt"

	"github.com/dativa-lv/trust-cache-worker/internal/schedule"
)

func TestRunsImmediatelyThenOnTicks(t *testing.T) {
	var runs atomic.Int32
	var lastCID atomic.Value
	task := schedule.New("test-job", 20*time.Millisecond, func(ctx context.Context) {
		runs.Add(1)
		lastCID.Store(propagation.CorrelationID(ctx))
	})
	qt.Assert(t, qt.Equals(task.Name(), "test-job"))
	qt.Assert(t, qt.IsNil(task.Start(context.Background())))
	defer task.Stop()

	deadline := time.After(2 * time.Second)
	for runs.Load() < 3 {
		select {
		case <-deadline:
			t.Fatalf("job ran %d times, want >= 3", runs.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	// Background cycles mint a correlation id (go-platform-kit propagation)
	// so one cycle's outbound calls and logs join up.
	cid, _ := lastCID.Load().(string)
	qt.Check(t, qt.Not(qt.Equals(cid, "")))
}

func TestStopHalts(t *testing.T) {
	var runs atomic.Int32
	task := schedule.New("test-job", 10*time.Millisecond, func(context.Context) { runs.Add(1) })
	qt.Assert(t, qt.IsNil(task.Start(context.Background())))
	time.Sleep(35 * time.Millisecond)
	task.Stop()
	n := runs.Load()
	time.Sleep(50 * time.Millisecond)
	qt.Check(t, qt.Equals(runs.Load(), n)) // no runs after Stop
}

// TestStopIsIdempotent guards against a "close of closed channel" panic if
// Stop is ever called more than once (e.g. a shutdown path that calls Stop
// on every registered Tasker without tracking which ones already stopped).
func TestStopIsIdempotent(t *testing.T) {
	task := schedule.New("test-job", 10*time.Millisecond, func(context.Context) {})
	qt.Assert(t, qt.IsNil(task.Start(context.Background())))
	task.Stop()
	task.Stop() // must not panic
}
