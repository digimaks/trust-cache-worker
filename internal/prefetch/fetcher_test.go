package prefetch_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/dativa-lv/trust-cache-worker/internal/prefetch"
)

// fetchTimeout keeps a hung server (or a client bug) failing the test fast
// rather than hanging the suite.
const fetchTimeout = 2 * time.Second

func TestHTTPFetcherGetSuccess(t *testing.T) {
	body := []byte("status-list-token-bytes")
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	f := prefetch.NewHTTPFetcher(1<<20, fetchTimeout)
	got, err := f.Get(context.Background(), srv.URL)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(got, body))
	// The fetcher's Accept header is load-bearing: it selects the JWT status
	// list representation over other content types.
	qt.Check(t, qt.Equals(gotAccept, "application/statuslist+jwt"))
}

func TestHTTPFetcherGetNonOKStatusErrorsWithoutPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	f := prefetch.NewHTTPFetcher(1<<20, fetchTimeout)
	got, err := f.Get(context.Background(), srv.URL)
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsNil(got))
}

func TestHTTPFetcherGetExceedsMaxBytesCap(t *testing.T) {
	big := make([]byte, 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	// maxBytes far below the served body: the io.LimitReader(maxBytes+1) cap
	// still lets it read one byte over, then Get must reject it.
	f := prefetch.NewHTTPFetcher(16, fetchTimeout)
	got, err := f.Get(context.Background(), srv.URL)
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsNil(got))
}

func TestHTTPFetcherGetBadURLFailsRequestBuild(t *testing.T) {
	f := prefetch.NewHTTPFetcher(1<<20, fetchTimeout)
	// A control character in the URL fails http.NewRequestWithContext's
	// parse, exercising the "build request" error branch.
	got, err := f.Get(context.Background(), "http://\x7f")
	qt.Check(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsNil(got))
}

// TestHTTPFetcherGetTransportErrorOmitsRawURL: a dial failure surfaces as a
// *url.Error whose Error() embeds the request URL — Get must strip that
// before returning, so callers logging zap.Error(err) never leak a raw
// status-list issuer URI (list_key is hashed everywhere else).
func TestHTTPFetcherGetTransportErrorOmitsRawURL(t *testing.T) {
	const uri = "http://127.0.0.1:1/statuslists/secret-issuer-path"
	f := prefetch.NewHTTPFetcher(1<<20, fetchTimeout)
	got, err := f.Get(context.Background(), uri)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Check(t, qt.IsNil(got))
	qt.Check(t, qt.IsFalse(strings.Contains(err.Error(), uri)))
}
