// Package mocktrust is an httptest mock of the trust-anchor service's
// verifier-facing API: strong-ETag anchors.json with If-None-Match→304,
// X-Trust-Snapshot/X-Trust-Stale headers, and /v1/snapshot. Fixtures
// mirror the trust-anchor service's response shapes; this mock mirrors
// its API shapes.
package mocktrust

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

//go:embed testdata/*.json
var fixtures embed.FS

// RecordedRequest captures what the worker sent — auth assertions and
// ETag assertions read these.
type RecordedRequest struct {
	Path          string
	Type          string // ?type= query (extension)
	IfNoneMatch   string
	Authorization string
	DPoP          string
	CorrelationID string
}

// Server is the mock. Safe for concurrent use.
type Server struct {
	URL string

	mu       sync.Mutex
	etag     string
	anchors  []byte
	snapshot []byte
	reqs     []RecordedRequest
	stale    bool
	certDER  []byte

	ts *httptest.Server
}

// New starts a mock preloaded with the default fixtures and a freshly
// generated EC certificate (test certs are generated in-test, never
// committed — testdata carries only {{CERT_DER}} placeholders).
func New(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{}
	der := generateCertDER(tb)
	s.certDER = der
	s.SetAnchors("snap-0001", RenderFixture(tb, "anchors_pid_provider_v1.json", der))
	s.SetSnapshot(Fixture(tb, "snapshot_ok.json"))

	s.ts = httptest.NewServer(http.HandlerFunc(s.handle))
	s.URL = s.ts.URL
	tb.Cleanup(s.ts.Close)
	return s
}

// SetAnchors swaps the served bundle + its snapshot id (strong ETag).
func (s *Server) SetAnchors(etag string, body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.etag, s.anchors = etag, body
}

// SwapSnapshotID re-serves the default bundle under a new snapshot id —
// body and ETag together, the way the real service's identity moves when
// content changes. Tests that must observe propagation use this: the id is
// carried in the body too, so swapping the ETag alone would serve a bundle
// still claiming the old identity.
func (s *Server) SwapSnapshotID(tb testing.TB, id string) {
	tb.Helper()
	s.mu.Lock()
	der := s.certDER
	s.mu.Unlock()
	body := bytes.ReplaceAll(RenderFixture(tb, "anchors_pid_provider_v1.json", der),
		[]byte("snap-0001"), []byte(id))
	s.SetAnchors(id, body)
}

// SetSnapshot swaps the /v1/snapshot body.
func (s *Server) SetSnapshot(body []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = body
}

// SetStale flips the X-Trust-Stale header.
func (s *Server) SetStale(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stale = v
}

// Requests returns a copy of everything recorded so far.
func (s *Server) Requests() []RecordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]RecordedRequest, len(s.reqs))
	copy(out, s.reqs)
	return out
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.reqs = append(s.reqs, RecordedRequest{
		Path:          r.URL.Path,
		Type:          r.URL.Query().Get("type"),
		IfNoneMatch:   r.Header.Get("If-None-Match"),
		Authorization: r.Header.Get("Authorization"),
		DPoP:          r.Header.Get("DPoP"),
		CorrelationID: r.Header.Get("X-Correlation-ID"),
	})
	etag, anchors, snapshot, stale := s.etag, s.anchors, s.snapshot, s.stale
	s.mu.Unlock()

	staleHdr := "false"
	if stale {
		staleHdr = "true"
	}
	w.Header().Set("X-Trust-Snapshot", etag)
	w.Header().Set("X-Trust-Stale", staleHdr)

	switch r.URL.Path {
	case "/v1/anchors.json":
		w.Header().Set("ETag", `"`+etag+`"`)
		if inm := r.Header.Get("If-None-Match"); inm != "" && strings.Trim(strings.TrimSpace(inm), `"`) == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(anchors)
	case "/v1/snapshot":
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(snapshot)
	default:
		http.NotFound(w, r)
	}
}

// Fixture returns a raw embedded fixture by base name.
func Fixture(tb testing.TB, name string) []byte {
	tb.Helper()
	raw, err := fixtures.ReadFile("testdata/" + name)
	if err != nil {
		tb.Fatal(err)
	}
	return raw
}

// RenderFixture fills the {{CERT_DER}}/{{CERT_FP}} placeholders.
func RenderFixture(tb testing.TB, name string, certDER []byte) []byte {
	tb.Helper()
	fp := sha256.Sum256(certDER)
	out := string(Fixture(tb, name))
	out = strings.ReplaceAll(out, "{{CERT_DER}}", base64.StdEncoding.EncodeToString(certDER))
	out = strings.ReplaceAll(out, "{{CERT_FP}}", hex.EncodeToString(fp[:]))
	return []byte(out)
}

func generateCertDER(tb testing.TB) []byte {
	tb.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "EUDI Test PID CA"},
		NotBefore:             time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		NotAfter:              time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		tb.Fatal(err)
	}
	return der
}
