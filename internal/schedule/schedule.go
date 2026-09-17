// Package schedule is the worker's shared background-loop runner: a
// core.Tasker that runs the job once immediately, then on
// every tick, minting a per-cycle correlation id (go-platform-kit propagation
// — background jobs have no inbound request to inherit from).
package schedule

import (
	"context"
	"sync"
	"time"

	"azugo.io/core"
	"github.com/gmb-lib/go-platform-kit/propagation"
	"github.com/oklog/ulid/v2"
)

type task struct {
	name     string
	interval time.Duration
	job      func(ctx context.Context)
	ticker   *time.Ticker
	stop     chan struct{}
	stopOnce sync.Once
}

// New builds a ticker task. The job owns its error handling (log + metrics);
// a panic in a job is a bug — jobs must not panic (they only orchestrate).
func New(name string, interval time.Duration, job func(ctx context.Context)) core.Tasker {
	return &task{name: name, interval: interval, job: job}
}

func (t *task) Name() string { return t.name }

func (t *task) Start(ctx context.Context) error {
	t.stop = make(chan struct{})
	t.ticker = time.NewTicker(t.interval)
	go func() {
		t.runOnce(ctx) // immediate first cycle: a fresh pod warms the cache now
		for {
			select {
			case <-t.stop:
				return
			case <-t.ticker.C:
				t.runOnce(ctx)
			}
		}
	}()
	return nil
}

func (t *task) runOnce(ctx context.Context) {
	// One correlation id per cycle: outbound HTTP (X-Correlation-ID) and every
	// log line of the cycle join up.
	t.job(propagation.WithCorrelationID(ctx, ulid.Make().String()))
}

// Stop halts future runs. Safe to call more than once; safe to call
// concurrently with the running loop — it never mutates t.ticker (only reads
// it once via Ticker.Stop, which is documented safe for concurrent use), so
// there is no data race with the goroutine's own read of t.ticker.C.
func (t *task) Stop() {
	t.stopOnce.Do(func() {
		if t.ticker != nil {
			t.ticker.Stop()
		}
		close(t.stop)
	})
}
