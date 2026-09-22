package prefetch_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
	"go.uber.org/zap"

	"github.com/digimaks/trust-cache-worker/internal/prefetch"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

type fakeRefs []string

func (f fakeRefs) TopStatusRefs(_ context.Context, n int) ([]string, error) {
	if n > len(f) {
		n = len(f)
	}
	return f[:n], nil
}

type fakeFetcher struct {
	responses map[string][]byte
	calls     []string
}

func (f *fakeFetcher) Get(_ context.Context, url string) ([]byte, error) {
	f.calls = append(f.calls, url)
	body, ok := f.responses[url]
	if !ok {
		return nil, errors.New("fetch failed")
	}
	return body, nil
}

type fakeStatusStore struct {
	ttls    map[string]time.Duration
	writes  map[string][]byte
	setTTLs map[string]time.Duration
	trimmed int
}

func newFakeStatusStore() *fakeStatusStore {
	return &fakeStatusStore{ttls: map[string]time.Duration{}, writes: map[string][]byte{}, setTTLs: map[string]time.Duration{}}
}

func (s *fakeStatusStore) GetTTL(_ context.Context, key string) (time.Duration, bool, error) {
	ttl, ok := s.ttls[key]
	return ttl, ok, nil
}

func (s *fakeStatusStore) SetBytes(_ context.Context, key string, val []byte, ttl time.Duration) error {
	s.writes[key] = val
	s.setTTLs[key] = ttl
	s.ttls[key] = ttl
	return nil
}

func (s *fakeStatusStore) TrimStatusRefs(_ context.Context, keep int) error {
	s.trimmed = keep
	return nil
}

func cfg() prefetch.Configuration {
	return prefetch.Configuration{
		TopN: 10, Interval: time.Minute, DefaultTTL: 5 * time.Minute,
		MinTTL: 30 * time.Second, MaxTTL: 24 * time.Hour, MaxBytes: 1 << 20,
	}
}

func TestRunRefreshesStaleAndSkipsFresh(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	freshURI := "https://issuer.example/statuslists/fresh"
	staleURI := "https://issuer.example/statuslists/stale"

	store := newFakeStatusStore()
	// fresh: remaining TTL comfortably beyond the next cycle — must NOT refetch.
	store.ttls[trustcache.StatusListKey(freshURI)] = 10 * time.Minute

	token := jwtWith(t, map[string]any{"ttl": 600})
	fetch := &fakeFetcher{responses: map[string][]byte{staleURI: token}}

	p := prefetch.New(fakeRefs{freshURI, staleURI}, fetch, store, cfg(), zap.NewNop(), func() time.Time { return now })
	qt.Assert(t, qt.IsNil(p.Run(context.Background())))

	qt.Check(t, qt.DeepEquals(fetch.calls, []string{staleURI})) // hit skipped, miss fetched
	key := trustcache.StatusListKey(staleURI)
	qt.Check(t, qt.DeepEquals(store.writes[key], token))
	// Prefetch respects the list ttl claim: 600 s.
	qt.Check(t, qt.Equals(store.setTTLs[key], 10*time.Minute))
	qt.Check(t, qt.Equals(store.trimmed, 1000))
}

func TestRunClampsTTL(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	uri := "https://issuer.example/statuslists/1"
	store := newFakeStatusStore()

	tests := []struct {
		name  string
		token []byte
		want  time.Duration
	}{
		{"huge_ttl_clamped_to_max", jwtWith(t, map[string]any{"ttl": 999999999}), 24 * time.Hour},
		{"tiny_ttl_clamped_to_min", jwtWith(t, map[string]any{"ttl": 1}), 30 * time.Second},
		{"expired_clamped_to_min", jwtWith(t, map[string]any{"exp": now.Add(-time.Hour).Unix()}), 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetch := &fakeFetcher{responses: map[string][]byte{uri: tt.token}}
			p := prefetch.New(fakeRefs{uri}, fetch, store, cfg(), zap.NewNop(), func() time.Time { return now })
			qt.Assert(t, qt.IsNil(p.Run(context.Background())))
			qt.Check(t, qt.Equals(store.setTTLs[trustcache.StatusListKey(uri)], tt.want))
			delete(store.ttls, trustcache.StatusListKey(uri))
		})
	}
}

// A failing fetch must not abort the cycle for the remaining URIs.
func TestRunContinuesPastFetchFailure(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	bad := "https://issuer.example/statuslists/bad"
	good := "https://issuer.example/statuslists/good"
	store := newFakeStatusStore()
	fetch := &fakeFetcher{responses: map[string][]byte{good: jwtWith(t, map[string]any{"ttl": 300})}}

	p := prefetch.New(fakeRefs{bad, good}, fetch, store, cfg(), zap.NewNop(), func() time.Time { return now })
	qt.Assert(t, qt.IsNil(p.Run(context.Background())))
	qt.Check(t, qt.IsTrue(len(store.writes[trustcache.StatusListKey(good)]) > 0))
	qt.Check(t, qt.Equals(len(store.writes[trustcache.StatusListKey(bad)]), 0))
}

func TestRunTopNZeroIsNoop(t *testing.T) {
	c := cfg()
	c.TopN = 0
	fetch := &fakeFetcher{}
	p := prefetch.New(fakeRefs{"https://x"}, fetch, newFakeStatusStore(), c, zap.NewNop(), nil)
	qt.Assert(t, qt.IsNil(p.Run(context.Background())))
	qt.Check(t, qt.Equals(len(fetch.calls), 0))
}
