// Package trustcacheworker is the verifier's trust-cache worker service: it
// feeds the fleet-shared Valkey trust cache (anchors, status lists, freshness
// telemetry) from the central trust-anchor service. No public surface.
package trustcacheworker

import (
	"context"
	"fmt"

	"azugo.io/azugo"
	"azugo.io/azugo/server"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	trust "github.com/gmb-eudi/go-eudi-trust"
	pkerrors "github.com/gmb-lib/go-platform-kit/errors"
	"github.com/gmb-lib/go-platform-kit/platform"

	"github.com/digimaks/trust-cache-worker/internal/authdoer"
	"github.com/digimaks/trust-cache-worker/internal/health"
	"github.com/digimaks/trust-cache-worker/internal/prefetch"
	"github.com/digimaks/trust-cache-worker/internal/schedule"
	"github.com/digimaks/trust-cache-worker/internal/telemetry"
	"github.com/digimaks/trust-cache-worker/internal/trustsync"
	"github.com/digimaks/trust-cache-worker/internal/valkeystore"
)

// App is the trust-cache-worker application container.
type App struct {
	*azugo.App

	config *Configuration
	state  *health.State
	store  *valkeystore.Store
	client trust.Client
	syncer *trustsync.Syncer

	// allowColdStart mirrors the --allow-cold-start flag: without it, an
	// unreachable trust service at startup is fatal.
	allowColdStart bool
}

// New builds the trust-cache-worker App: server.New wires Azugo, then init
// layers platform.Setup and the readiness state on top.
func New(cmd *cobra.Command, version string) (*App, error) {
	config := NewConfiguration()

	a, err := server.New(cmd, server.Options{
		AppName:       "Trust Cache Worker",
		AppVer:        version,
		Configuration: config,
	})
	if err != nil {
		return nil, err
	}

	app := &App{App: a, config: config}
	if cmd != nil {
		if v, err := cmd.Flags().GetBool("allow-cold-start"); err == nil {
			app.allowColdStart = v
		}
	}
	if err := app.init(); err != nil {
		return nil, err
	}
	return app, nil
}

func (a *App) init() error {
	// Verifier error taxonomy: register at startup, before platform.Setup,
	// from this single site. This worker
	// surfaces trust degradation on /readyz rather than as problem responses,
	// but the reason is registered so any future handler maps identically to
	// eudi-verifier-core's err:trust:anchor-unavailable (503).
	pkerrors.RegisterReason("anchorUnavailable", pkerrors.ReasonSpec{Status: 503, Title: "Trust anchors unavailable"})

	// One Setup call wires tracing, redaction, correlation and the problem
	// renderer. No public surface here → PublicErrors stays false (the full
	// internal error envelope is used).
	if err := platform.Setup(a.App, platform.Options{
		Config: a.config.BaseConfiguration,
	}); err != nil {
		return err
	}

	// Ready only once every configured anchor type has synced fresh
	// (fail closed). The gauges register on the shared
	// VictoriaMetrics registry Azugo serves at /metrics.
	a.state = health.NewState(a.config.Sync.Types, nil)
	a.state.RegisterMetrics()

	store, err := valkeystore.New(valkeystore.Options{
		URL:       a.config.ValkeyURL,
		Password:  a.config.ValkeyPassword,
		KeyPrefix: a.config.ValkeyKeyPrefix,
	})
	if err != nil {
		return err // Valkey is a core dependency: fail fast, the pod restarts
	}
	a.store = store

	// dpop mode bypasses authdoer.New/RequestSigner entirely: go-authbyte owns
	// the whole request execution (no per-request signing primitive exists —
	// see authbyte.go). internal mode is the plain instrumented http.Client
	// path, unchanged.
	var doer authdoer.Doer
	if a.config.Auth.Mode == authdoer.ModeDPoP {
		doer, err = authdoer.NewAuthbyteDoer(*a.config.Auth)
		if err != nil {
			return err
		}
	} else {
		doer, err = authdoer.New(*a.config.Auth, nil)
		if err != nil {
			return err
		}
	}

	// go-eudi-trust client over the injected doer; this is the single
	// construction site for the client.
	a.client, err = trust.NewClient(a.config.TrustServiceURL, doer)
	if err != nil {
		return err
	}

	a.syncer = trustsync.New(a.client, store, a.state, *a.config.Sync, a.Log().Named("trustsync"), nil)
	// The scheduled poll and the operator resync route share Syncer.Run, so
	// the two can never run concurrent cycles — a caller arriving mid-cycle
	// joins the one in flight.
	if err := a.AddTask(schedule.New("trust-sync", a.config.Sync.PollInterval, func(ctx context.Context) {
		_ = a.syncer.Run(ctx) // Run only errors on the caller's own context expiry
	})); err != nil {
		return err
	}

	poller := telemetry.New(a.client, store, a.state, 3*a.config.Sync.SnapshotPollInterval, a.Log().Named("telemetry"), nil)
	if err := a.AddTask(schedule.New("trust-snapshot", a.config.Sync.SnapshotPollInterval, func(ctx context.Context) {
		if err := poller.Poll(ctx); err != nil {
			a.Log().Error("snapshot telemetry poll failed", zap.Error(err))
		}
	})); err != nil {
		return err
	}

	pf := prefetch.New(store, prefetch.NewHTTPFetcher(a.config.Prefetch.MaxBytes, a.config.Auth.Timeout),
		store, *a.config.Prefetch, a.Log().Named("prefetch"), nil)
	if err := a.AddTask(schedule.New("status-prefetch", a.config.Prefetch.Interval, func(ctx context.Context) {
		if err := pf.Run(ctx); err != nil {
			a.Log().Error("status-list prefetch failed", zap.Error(err))
		}
	})); err != nil {
		return err
	}

	return nil
}

// Start enforces the startup contract, then starts the server + tasks.
func (a *App) Start() error {
	if err := a.startupCheck(); err != nil {
		return err
	}
	return a.App.Start()
}

// startupCheck probes the trust service once. Unreachable => fatal, unless
// --allow-cold-start (worker boots degraded; /readyz 503 until first sync).
func (a *App) startupCheck() error {
	ctx, cancel := context.WithTimeout(a.BackgroundContext(), a.config.Auth.Timeout)
	defer cancel()
	if _, err := a.client.Snapshot(ctx); err != nil {
		if !a.allowColdStart {
			return fmt.Errorf("trust-cache-worker: trust service unreachable at startup (restart with --allow-cold-start to boot degraded): %w", err)
		}
		a.Log().Warn("trust service unreachable at startup — cold start allowed; /readyz stays 503 until the first successful sync",
			zap.Error(err))
	}
	return nil
}

// Config returns application configuration. Panics if not loaded.
func (a *App) Config() *Configuration {
	if a.config == nil || !a.config.Ready() {
		panic("configuration is not loaded")
	}
	return a.config
}

// State exposes readiness state to the routes package.
func (a *App) State() *health.State { return a.state }

// Syncer exposes the anchor syncer to the routes package (operator resync).
func (a *App) Syncer() *trustsync.Syncer { return a.syncer }
