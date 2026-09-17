// Package valkeystore is the worker's write-side of the trust cache: a thin
// valkey-go wrapper enforcing the trustcache key contract — TTL on every
// key (fail-closed) and atomic per-type replacement via MULTI/EXEC (the
// snapshot is authoritative, disappeared anchors purge with the swap).
package valkeystore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	valkey "github.com/valkey-io/valkey-go"

	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

// Store wraps the shared Valkey. Safe for concurrent use.
type Store struct {
	client valkey.Client
	// prefix is prepended to every key this store touches ("" = none). It is
	// an environment fact — a managed instance commonly confines a user's ACL
	// to one key pattern — not part of the cross-service key contract, which
	// stays the literal trustcache names. Every consumer of the same cache
	// must be configured with the same value.
	prefix string
}

// Options configures the connection and the keyspace.
type Options struct {
	// URL of the shared Valkey/Redis: redis:// or rediss:// (TLS), with an
	// optional user, password, database index and query options — the same
	// syntax the other services in the fleet take. A bare host:port is still
	// accepted and treated as redis://host:port so existing deployments keep
	// working.
	URL string
	// Password, when set, overrides any password carried in the URL. It is
	// separate so a platform can mount the secret as a file and never put it
	// into a URL that shows up in process listings or events.
	Password string
	// KeyPrefix is prepended to every key as "<KeyPrefix>:"; a trailing ":" in
	// the configured value is tolerated. Empty means no prefix.
	KeyPrefix string
}

// ErrEmptyURL reports an Options without a URL.
var ErrEmptyURL = errors.New("valkeystore: empty URL")

// New dials Valkey per Options.
//
// Client-side caching is disabled: the worker is the writer and gains nothing
// from it, and the unit-test server (miniredis) does not implement CLIENT
// TRACKING. ForceSingleClient pins single-node (non-cluster) semantics: the
// trust cache is one shared instance and the atomic per-type swap MULTI/EXECs
// keys that hash to different slots (trust:anchors:<type>:<terr>,
// trust:anchors:territories:<type>, trust:anchors:etag:<type>,
// trust:freshness:<type>) — a cluster client rejects that ("cross slot command
// in Dedicated is prohibited"). The keys carry no shared hash tag by contract,
// so cross-key atomicity is only well-defined on a single node; forcing it
// also makes behavior deterministic regardless of how the server answers
// cluster probes (miniredis answers them and would otherwise leave valkey-go
// in cluster mode).
func New(opt Options) (*Store, error) {
	copt, err := clientOption(opt)
	if err != nil {
		return nil, err
	}
	c, err := valkey.NewClient(copt)
	if err != nil {
		return nil, fmt.Errorf("valkeystore: connect %s: %w", strings.Join(copt.InitAddress, ","), err)
	}
	return &Store{client: c, prefix: keyPrefix(opt.KeyPrefix)}, nil
}

// clientOption turns Options into the driver's ClientOption. A URL without a
// scheme is a bare host:port and gets redis:// prepended; anything else must
// parse as a redis:// or rediss:// URL. The password option, when set, wins
// over one embedded in the URL.
func clientOption(opt Options) (valkey.ClientOption, error) {
	raw := strings.TrimSpace(opt.URL)
	if raw == "" {
		return valkey.ClientOption{}, ErrEmptyURL
	}
	if !strings.Contains(raw, "://") {
		raw = "redis://" + raw
	}
	copt, err := valkey.ParseURL(raw)
	if err != nil {
		return valkey.ClientOption{}, fmt.Errorf("valkeystore: parse URL: %w", err)
	}
	if opt.Password != "" {
		copt.Password = opt.Password
	}
	copt.DisableCache = true
	copt.ForceSingleClient = true
	return copt, nil
}

// keyPrefix normalizes the configured prefix to "<p>:" or "".
func keyPrefix(p string) string {
	p = strings.TrimRight(strings.TrimSpace(p), ":")
	if p == "" {
		return ""
	}
	return p + ":"
}

// key applies the configured prefix to a contract key.
func (s *Store) key(k string) string { return s.prefix + k }

// Close releases the client.
func (s *Store) Close() { s.client.Close() }

// Get implements trustcache.Getter: (nil, nil) when the key is absent.
func (s *Store) Get(ctx context.Context, key string) ([]byte, error) {
	resp := s.client.Do(ctx, s.client.B().Get().Key(s.key(key)).Build())
	if err := resp.Error(); err != nil {
		if valkey.IsValkeyNil(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("valkeystore: get %s: %w", key, err)
	}
	b, err := resp.AsBytes()
	if err != nil {
		return nil, fmt.Errorf("valkeystore: get %s: %w", key, err)
	}
	return b, nil
}

// SetJSON marshals v and stores it with the given TTL (TTL is mandatory —
// every trust-cache key expires).
func (s *Store) SetJSON(ctx context.Context, key string, v any, ttl time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("valkeystore: marshal %s: %w", key, err)
	}
	return s.SetBytes(ctx, key, raw, ttl)
}

// SetBytes stores raw bytes with the given TTL.
func (s *Store) SetBytes(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("valkeystore: set %s: non-positive ttl — every key must expire", key)
	}
	cmd := s.client.B().Set().Key(s.key(key)).Value(string(val)).Ex(ttl).Build()
	if err := s.client.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("valkeystore: set %s: %w", key, err)
	}
	return nil
}

// GetTTL returns the remaining TTL of a key (exists=false when absent).
func (s *Store) GetTTL(ctx context.Context, key string) (time.Duration, bool, error) {
	ms, err := s.client.Do(ctx, s.client.B().Pttl().Key(s.key(key)).Build()).AsInt64()
	if err != nil {
		return 0, false, fmt.Errorf("valkeystore: pttl %s: %w", key, err)
	}
	if ms == -2 { // key does not exist
		return 0, false, nil
	}
	if ms == -1 { // exists without TTL — should never happen for our keys
		return 0, true, nil
	}
	return time.Duration(ms) * time.Millisecond, true, nil
}

// Swap is one atomic per-type replacement (a changed snapshot ETag).
type Swap struct {
	KeyType   string
	ETag      string
	Entries   []trustcache.AnchorSetEntry // one per territory, already normalized
	Freshness trustcache.TypeFreshness
	TTL       time.Duration
}

// SwapAnchorType replaces a type's whole materialization in one MULTI/EXEC:
// vanished territory keys are DELed and every entry/index/etag/freshness key
// SET in the same transaction, so readers observe strictly before- or
// strictly after-swap state. MULTI is used rather than a
// versioned-key/pointer-swap. The snapshot is authoritative; [ARF §6.6.3.6]
// anchor withdrawal takes effect on snapshot replacement.
func (s *Store) SwapAnchorType(ctx context.Context, sw Swap) error {
	prev, err := s.territories(ctx, sw.KeyType)
	if err != nil {
		return err
	}

	next := make([]string, 0, len(sw.Entries))
	nextSet := make(map[string]struct{}, len(sw.Entries))
	for _, e := range sw.Entries {
		nextSet[e.Territory] = struct{}{}
		next = append(next, e.Territory)
	}
	sort.Strings(next)

	idxRaw, err := json.Marshal(next)
	if err != nil {
		return fmt.Errorf("valkeystore: marshal territory index: %w", err)
	}
	frRaw, err := json.Marshal(sw.Freshness)
	if err != nil {
		return fmt.Errorf("valkeystore: marshal freshness: %w", err)
	}

	return s.client.Dedicated(func(c valkey.DedicatedClient) error {
		cmds := make([]valkey.Completed, 0, len(prev)+len(sw.Entries)+5)
		cmds = append(cmds, c.B().Multi().Build())
		for _, t := range prev {
			if _, ok := nextSet[t]; !ok {
				cmds = append(cmds, c.B().Del().Key(s.key(trustcache.AnchorSetKey(sw.KeyType, t))).Build())
			}
		}
		for _, e := range sw.Entries {
			raw, err := json.Marshal(e)
			if err != nil {
				return fmt.Errorf("valkeystore: marshal entry %s/%s: %w", sw.KeyType, e.Territory, err)
			}
			cmds = append(cmds, c.B().Set().Key(s.key(trustcache.AnchorSetKey(sw.KeyType, e.Territory))).
				Value(string(raw)).Ex(sw.TTL).Build())
		}
		cmds = append(cmds,
			c.B().Set().Key(s.key(trustcache.TerritoryIndexKey(sw.KeyType))).Value(string(idxRaw)).Ex(sw.TTL).Build(),
			c.B().Set().Key(s.key(trustcache.ETagKey(sw.KeyType))).Value(sw.ETag).Ex(sw.TTL).Build(),
			c.B().Set().Key(s.key(trustcache.FreshnessKey(sw.KeyType))).Value(string(frRaw)).Ex(sw.TTL).Build(),
			c.B().Exec().Build(),
		)
		for _, resp := range c.DoMulti(ctx, cmds...) {
			if err := resp.Error(); err != nil {
				return fmt.Errorf("valkeystore: swap %s: %w", sw.KeyType, err)
			}
		}
		return nil
	})
}

// RefreshAnchorType is the 304 path (cheap — nothing is rewritten when the
// snapshot is unchanged): the anchor CONTENT (the certificates themselves) is
// never rewritten or deleted, but every anchor-set entry's embedded validUntil is
// advanced to fr.ValidUntil — the same cache-validity horizon
// trustsync.SyncType computes for a 200 (now + CacheTTL) — because
// trustcache/keys.go documents the two as always naming the same instant
// ("TTL = the entry's ValidUntil horizon"). A 304 is upstream's explicit
// confirmation the snapshot is still current, so that horizon must advance
// exactly as it would on a 200; leaving it frozen while only the raw key's
// TTL is extended lets a consumer's embedded-ValidUntil fail-closed check
// (the wallet-facing verifier's AnchorsFor) trip on a perfectly healthy,
// successfully polling worker — the steady-state fail-closed trap this fixes.
// TTLs on the territory-index/etag keys and the freshness key are extended as
// before (they carry no embedded validUntil of their own).
func (s *Store) RefreshAnchorType(ctx context.Context, keyType string, fr trustcache.TypeFreshness, ttl time.Duration) error {
	prev, err := s.territories(ctx, keyType)
	if err != nil {
		return err
	}

	// Read + patch each territory's entry outside the transaction (the same
	// discipline SwapAnchorType uses for its marshaled values); MULTI/EXEC
	// commands queue blindly, so the read-decode-patch-encode has to happen
	// before the transaction is built. A key that vanished between
	// s.territories() and here (rare TTL race, not the steady-state case)
	// is left out of patched and falls back to a plain EXPIRE below — a
	// no-op on an absent key, never a resurrection.
	patched := make(map[string][]byte, len(prev))
	for _, t := range prev {
		raw, err := s.Get(ctx, trustcache.AnchorSetKey(keyType, t))
		if err != nil {
			return err
		}
		if raw == nil {
			continue
		}
		var e trustcache.AnchorSetEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("valkeystore: decode entry %s/%s: %w", keyType, t, err)
		}
		e.ValidUntil = fr.ValidUntil
		out, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("valkeystore: marshal entry %s/%s: %w", keyType, t, err)
		}
		patched[t] = out
	}

	frRaw, err := json.Marshal(fr)
	if err != nil {
		return fmt.Errorf("valkeystore: marshal freshness: %w", err)
	}
	return s.client.Dedicated(func(c valkey.DedicatedClient) error {
		secs := int64(ttl / time.Second)
		cmds := make([]valkey.Completed, 0, len(prev)+4)
		cmds = append(cmds, c.B().Multi().Build())
		for _, t := range prev {
			key := s.key(trustcache.AnchorSetKey(keyType, t))
			if raw, ok := patched[t]; ok {
				cmds = append(cmds, c.B().Set().Key(key).Value(string(raw)).Ex(ttl).Build())
			} else {
				cmds = append(cmds, c.B().Expire().Key(key).Seconds(secs).Build())
			}
		}
		cmds = append(cmds,
			c.B().Expire().Key(s.key(trustcache.TerritoryIndexKey(keyType))).Seconds(secs).Build(),
			c.B().Expire().Key(s.key(trustcache.ETagKey(keyType))).Seconds(secs).Build(),
			c.B().Set().Key(s.key(trustcache.FreshnessKey(keyType))).Value(string(frRaw)).Ex(ttl).Build(),
			c.B().Exec().Build(),
		)
		for _, resp := range c.DoMulti(ctx, cmds...) {
			if err := resp.Error(); err != nil {
				return fmt.Errorf("valkeystore: refresh %s: %w", keyType, err)
			}
		}
		return nil
	})
}

// TopStatusRefs returns the n most recently referenced status-list URIs from
// the shared ZSET (score = unix seconds; written by the wallet-facing verifier).
func (s *Store) TopStatusRefs(ctx context.Context, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	cmd := s.client.B().Zrevrange().Key(s.key(trustcache.StatusRefsKey)).Start(0).Stop(int64(n - 1)).Build()
	uris, err := s.client.Do(ctx, cmd).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("valkeystore: status refs: %w", err)
	}
	return uris, nil
}

// TrimStatusRefs bounds the refs ZSET to the newest `keep` members. The stop
// rank -keep-1 removes ranks [0, len-keep-1] — the lowest-scored (oldest)
// members — leaving the newest `keep`. When len <= keep the resolved range is
// empty (start 0 > resolved end), so nothing is removed: real Redis/Valkey and
// miniredis both clamp a still-negative stop, and a "trim to keep N" must never
// trim when the set already fits. keep <= 0 is treated as "keep nothing".
func (s *Store) TrimStatusRefs(ctx context.Context, keep int) error {
	cmd := s.client.B().Zremrangebyrank().Key(s.key(trustcache.StatusRefsKey)).Start(0).Stop(int64(-keep - 1)).Build()
	if err := s.client.Do(ctx, cmd).Error(); err != nil {
		return fmt.Errorf("valkeystore: trim status refs: %w", err)
	}
	return nil
}

// territories reads the current territory index ([] when never written).
func (s *Store) territories(ctx context.Context, keyType string) ([]string, error) {
	raw, err := s.Get(ctx, trustcache.TerritoryIndexKey(keyType))
	if err != nil || raw == nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("valkeystore: decode territory index %s: %w", keyType, err)
	}
	return out, nil
}
