package prefetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gmb-lib/go-platform-kit/observability"
)

// Fetcher mirrors go-statuslist's Fetcher shape so the same seam serves both
// prefetch (here) and check-time fetches by the wallet-facing verifier.
type Fetcher interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

// NewHTTPFetcher builds the production fetcher: an OTel-instrumented
// http.Client (a bespoke client that bypasses ctx.HTTPClient()), response size
// cap, timeout. Status-list hosts are EXTERNAL issuer infrastructure: the
// correlation id is intentionally not propagated to them (same ruling as the
// trust-anchor service's external fetcher).
func NewHTTPFetcher(maxBytes int64, timeout time.Duration) Fetcher {
	return &httpFetcher{
		client:   observability.InstrumentHTTPClient(&http.Client{Timeout: timeout}),
		maxBytes: maxBytes,
	}
}

type httpFetcher struct {
	client   *http.Client
	maxBytes int64
}

func (f *httpFetcher) Get(ctx context.Context, listURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("prefetch: build request: %w", err)
	}
	req.Header.Set("Accept", "application/statuslist+jwt")
	resp, err := f.client.Do(req)
	if err != nil {
		// *url.Error.Error() embeds the raw request URL — strip it so a
		// transport failure never puts a status-list issuer URI into logs
		// (list_key is hashed everywhere else).
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return nil, fmt.Errorf("prefetch: fetch status list: %w", uerr.Err)
		}
		return nil, fmt.Errorf("prefetch: fetch status list: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prefetch: fetch status list: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("prefetch: read status list: %w", err)
	}
	if int64(len(body)) > f.maxBytes {
		return nil, fmt.Errorf("prefetch: status list exceeds %d bytes cap", f.maxBytes)
	}
	return body, nil
}
