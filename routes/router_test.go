package routes

import (
	"testing"
	"time"

	"azugo.io/azugo"
	"github.com/alicebob/miniredis/v2"
	"github.com/go-quicktest/qt"
	"github.com/valyala/fasthttp"

	worker "github.com/digimaks/trust-cache-worker"
	"github.com/digimaks/trust-cache-worker/internal/mocktrust"
)

func testApp(t testing.TB) *azugo.TestApp {
	t.Helper()
	app := worker.TestApp(t)
	err := Init(app)
	qt.Assert(t, qt.IsNil(err))
	return azugo.NewTestApp(app.App)
}

func TestHealthzOK(t *testing.T) {
	app := testApp(t)
	app.Start(t)
	defer app.Stop()

	resp, err := app.TestClient().Get("/healthz")
	qt.Assert(t, qt.IsNil(err))
	defer fasthttp.ReleaseResponse(resp)
	qt.Check(t, qt.Equals(resp.StatusCode(), fasthttp.StatusOK))
}

// Negative first (TDD): before any sync has ever run the worker must report
// NOT ready — fail closed, never "ready by default".
func TestReadyzNotReadyBeforeFirstSync(t *testing.T) {
	app := testApp(t)
	app.Start(t)
	defer app.Stop()

	resp, err := app.TestClient().Get("/readyz")
	qt.Assert(t, qt.IsNil(err))
	defer fasthttp.ReleaseResponse(resp)
	qt.Check(t, qt.Equals(resp.StatusCode(), fasthttp.StatusServiceUnavailable))
}

// The kit correlation middleware must be active (platform.Setup wired it):
// an inbound X-Correlation-ID is echoed on the response.
func TestCorrelationHeaderEchoed(t *testing.T) {
	app := testApp(t)
	app.Start(t)
	defer app.Stop()

	tc := app.TestClient()
	resp, err := tc.Get("/healthz", tc.WithHeader("X-Correlation-ID", "01TESTCID"))
	qt.Assert(t, qt.IsNil(err))
	defer fasthttp.ReleaseResponse(resp)
	qt.Check(t, qt.Equals(string(resp.Header.Peek("X-Correlation-ID")), "01TESTCID"))
}

// Acceptance (readyz-flip leg of the end-to-end sync): once the worker
// has materialized every configured anchor type against the mock trust service
// (miniredis-backed), /readyz flips from 503 to 200. Lives here rather than in
// app_test.go because that file is package trustcacheworker and importing
// routes (which imports the worker) from it would cycle.
func TestReadyzBecomesReadyAfterSync(t *testing.T) {
	mr := miniredis.RunT(t)
	mock := mocktrust.New(t)
	app := worker.TestAppWith(t, mock.URL, mr.Addr())
	qt.Assert(t, qt.IsNil(Init(app)))

	ta := azugo.NewTestApp(app.App)
	ta.Start(t) // starts the app incl. the AddTask'd sync loops → immediate first sync
	defer ta.Stop()

	// The mock serves the same bundle for every type= query, so every configured
	// type synces; readyz flips to 200 once all of them are fresh.
	deadline := time.After(5 * time.Second)
	for {
		resp, err := ta.TestClient().Get("/readyz")
		qt.Assert(t, qt.IsNil(err))
		code := resp.StatusCode()
		fasthttp.ReleaseResponse(resp)
		if code == fasthttp.StatusOK {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("readyz still %d after sync", code)
		case <-time.After(20 * time.Millisecond):
		}
	}
}
