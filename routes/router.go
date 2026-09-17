// Package routes registers the trust-cache-worker's HTTP surface: liveness
// and readiness probes, plus — only when an admin key is configured — the
// operator surface (trust identity + resync).
package routes

import (
	"crypto/sha256"

	worker "github.com/dativa-lv/trust-cache-worker"
)

type router struct {
	*worker.App
}

// Init registers the worker's HTTP surface: liveness + readiness, and the
// key-gated operator routes. With no admin key configured the /v1 group is
// not bound at all — the operator surface fails closed to nonexistent,
// never to unauthenticated.
func Init(a *worker.App) error {
	r := &router{App: a}

	a.Get("/healthz", r.healthz)
	a.Get("/readyz", r.readyz)

	if key := a.Config().TrustAdminKey; key != "" {
		v1 := a.Group("/v1")
		v1.Use(apiKeyAuth(sha256.Sum256([]byte(key))))
		v1.Get("/trust-status", r.trustStatus)
		v1.Post("/resync", r.resync)
	} else {
		a.Log().Info("no admin API key configured — the operator surface (/v1/trust-status, /v1/resync) is not served")
	}

	return nil
}
