package routes

import (
	"azugo.io/azugo"
	"github.com/valyala/fasthttp"
)

// healthz is the liveness probe: tiny {status} body, skipped in the access
// log.
func (r *router) healthz(ctx *azugo.Context) {
	ctx.SkipRequestLog()
	ctx.JSON(map[string]string{"status": "ok"})
}

// readyz reports cache-maintenance readiness. Not-ready until the first
// successful sync of every configured anchor type, and again when the cache
// goes stale — the same fail-closed posture eudi-verifier-core's /readyz applies
// when it consumes the trust:freshness:* keys.
func (r *router) readyz(ctx *azugo.Context) {
	ctx.SkipRequestLog()
	if ok, reason := r.State().Ready(); !ok {
		ctx.StatusCode(fasthttp.StatusServiceUnavailable)
		ctx.JSON(map[string]string{"status": "not-ready", "reason": reason})
		return
	}
	ctx.JSON(map[string]string{"status": "ok"})
}
