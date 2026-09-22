package routes

import (
	"crypto/sha256"
	"crypto/subtle"
	"time"

	"azugo.io/azugo"
	pkerrors "github.com/gmb-lib/go-platform-kit/errors"
	"github.com/valyala/fasthttp"

	"github.com/digimaks/trust-cache-worker/internal/health"
)

// apiKeyAuth gates the operator surface on an X-API-Key match — the same
// header shape the trust service's own admin endpoints use, so the operator's
// "refresh top-down, compare identities" runbook is one header for both hops.
// Both sides are hashed before comparing so the comparison is fixed-length
// (32 bytes either side) and never short-circuits on the presented value's
// own length. The key value itself never reaches a log, event, or error.
func apiKeyAuth(configuredHash [sha256.Size]byte) azugo.RequestHandlerFunc {
	return func(next azugo.RequestHandler) azugo.RequestHandler {
		return func(ctx *azugo.Context) {
			presentedHash := sha256.Sum256([]byte(ctx.Header.Get("X-API-Key")))
			if subtle.ConstantTimeCompare(presentedHash[:], configuredHash[:]) != 1 {
				ctx.Error(pkerrors.HTTP("admin-api", "unauthorized"))
				return
			}
			next(ctx)
		}
	}
}

// typeStatusBody is the wire shape of one anchor type's sync identity.
type typeStatusBody struct {
	Synced        bool       `json:"synced"`
	SnapshotID    string     `json:"snapshotId,omitempty"`
	FetchedAt     *time.Time `json:"fetchedAt,omitempty"`
	ValidUntil    *time.Time `json:"validUntil,omitempty"`
	UpstreamStale bool       `json:"upstreamStale,omitempty"`
}

// statusBody is the wire shape of GET /v1/trust-status and POST /v1/resync:
// the same identity report in both places, because the whole point of the
// resync answer is that the operator's next call is a comparison rather than
// a hope — an acknowledgement alone was measured to be true while the system
// stayed broken one hop later.
type statusBody struct {
	Ready  bool                      `json:"ready"`
	Reason string                    `json:"reason,omitempty"`
	Types  map[string]typeStatusBody `json:"types"`
}

func statusOf(state *health.State) statusBody {
	ready, reason := state.Ready()
	body := statusBody{Ready: ready, Reason: reason, Types: map[string]typeStatusBody{}}
	for _, st := range state.TypeStatuses() {
		tb := typeStatusBody{Synced: st.Synced}
		if st.Synced {
			tb.SnapshotID = st.SnapshotID
			fetched, valid := st.FetchedAt.UTC(), st.ValidUntil.UTC()
			tb.FetchedAt, tb.ValidUntil = &fetched, &valid
			tb.UpstreamStale = st.UpstreamStale
		}
		body.Types[st.Type] = tb
	}
	return body
}

// trustStatus reports the trust identity this worker currently maintains:
// per anchor type, the snapshot id it holds and how fresh it is. Read-only
// diagnostics — the counterpart of the trust service's snapshot identity,
// so an operator can confirm a change actually propagated to this hop.
func (r *router) trustStatus(ctx *azugo.Context) {
	ctx.JSON(statusOf(r.State()))
}

// resync runs one sync cycle now — or joins the cycle already in flight —
// and answers with the identity the worker holds after it, not a bare
// acknowledgement. The operator's cure for a stale cache is this call
// instead of a container restart.
func (r *router) resync(ctx *azugo.Context) {
	if err := r.Syncer().Run(ctx); err != nil {
		ctx.Error(err) // only the caller's own context expiring while waiting
		return
	}
	body := statusOf(r.State())
	if !body.Ready {
		// The cycle ran but the cache is still not fully fresh (an upstream
		// or per-type failure): report the same identity body under 503 so
		// the caller need not parse to notice. Which types lag is in the body.
		ctx.StatusCode(fasthttp.StatusServiceUnavailable)
	}
	ctx.JSON(body)
}
