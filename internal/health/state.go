// Package health tracks the worker's cache-maintenance state for /readyz.
// Fail closed by default: the worker is ready only when every configured
// anchor type has been synced and its cache entry is still valid.
package health

import (
	"sync"
	"time"

	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

type typeFreshness struct {
	snapshotID    string
	fetchedAt     time.Time
	validUntil    time.Time
	upstreamStale bool
}

// State is safe for concurrent use (sync loop writes, readyz reads).
type State struct {
	mu       sync.RWMutex
	now      func() time.Time
	required []string
	fresh    map[string]typeFreshness
	snap     *trustcache.SnapshotHealth
}

// NewState wants the wire anchor-type names the worker is configured to sync
// and an injectable clock. nil clock = time.Now.
func NewState(requiredTypes []string, now func() time.Time) *State {
	if now == nil {
		now = time.Now
	}
	return &State{now: now, required: requiredTypes, fresh: map[string]typeFreshness{}}
}

// SetTypeFreshness records a successful sync (200 or 304) for one anchor type.
func (s *State) SetTypeFreshness(keyType, snapshotID string, fetchedAt, validUntil time.Time, upstreamStale bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fresh[keyType] = typeFreshness{
		snapshotID:    snapshotID,
		fetchedAt:     fetchedAt,
		validUntil:    validUntil,
		upstreamStale: upstreamStale,
	}
	registerTypeGauge(keyType)
}

// TypeStatus is one configured anchor type's sync identity, for operator
// reporting: which snapshot the worker holds for that type and how fresh it
// is. Synced=false means the type has never synced in this process — the
// other fields are zero and must not be read as freshness.
type TypeStatus struct {
	Type          string
	Synced        bool
	SnapshotID    string
	FetchedAt     time.Time
	ValidUntil    time.Time
	UpstreamStale bool
}

// TypeStatuses reports every configured anchor type in configuration order,
// including the never-synced ones — an absent type is exactly what an
// operator diagnosing propagation needs to see.
func (s *State) TypeStatuses() []TypeStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]TypeStatus, 0, len(s.required))
	for _, t := range s.required {
		st := TypeStatus{Type: t}
		if fr, ok := s.fresh[t]; ok {
			st.Synced = true
			st.SnapshotID = fr.snapshotID
			st.FetchedAt = fr.fetchedAt
			st.ValidUntil = fr.validUntil
			st.UpstreamStale = fr.upstreamStale
		}
		out = append(out, st)
	}
	return out
}

// Ready reports whether the worker is maintaining a live cache for every
// configured type; reason is a short operator-safe explanation when not.
func (s *State) Ready() (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.required) == 0 {
		return false, "no anchor types configured"
	}
	now := s.now()
	for _, t := range s.required {
		fr, ok := s.fresh[t]
		if !ok {
			return false, "anchor type never synced: " + t
		}
		if !now.Before(fr.validUntil) {
			return false, "anchor cache expired for type: " + t
		}
	}
	return true, ""
}

// SetSnapshot records the latest snapshot telemetry and
// registers per-territory gauges on first sight of each territory.
func (s *State) SetSnapshot(h trustcache.SnapshotHealth) {
	s.mu.Lock()
	s.snap = &h
	s.mu.Unlock()
	for code := range h.Territories {
		registerTerritoryGauge(code)
	}
}

func (s *State) typeStalenessSeconds(keyType string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fr, ok := s.fresh[keyType]
	if !ok {
		return -1 // never synced — distinguishable from 0 (just synced)
	}
	return s.now().Sub(fr.fetchedAt).Seconds()
}

func (s *State) lotlSequence() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap == nil {
		return 0
	}
	return float64(s.snap.LOTLSequence)
}

func (s *State) upstreamStale() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap != nil && s.snap.UpstreamStale {
		return 1
	}
	return 0
}

func (s *State) pendingBootstrap() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap != nil && s.snap.PendingBootstrap {
		return 1
	}
	return 0
}

func (s *State) territoryStale(code string) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snap == nil {
		return 0
	}
	if t, ok := s.snap.Territories[code]; ok && t.Stale {
		return 1
	}
	return 0
}
