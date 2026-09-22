package prefetch_test

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/digimaks/trust-cache-worker/internal/prefetch"
)

func jwtWith(t testing.TB, claims map[string]any) []byte {
	t.Helper()
	enc := base64.RawURLEncoding
	h, err := json.Marshal(map[string]any{"alg": "ES256", "typ": "statuslist+jwt"})
	qt.Assert(t, qt.IsNil(err))
	p, err := json.Marshal(claims)
	qt.Assert(t, qt.IsNil(err))
	return []byte(enc.EncodeToString(h) + "." + enc.EncodeToString(p) + "." + enc.EncodeToString([]byte("sig")))
}

func TestPeekTTL(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	dflt := 5 * time.Minute

	tests := []struct {
		name  string
		token []byte
		want  time.Duration
	}{
		{"ttl_claim_honored", jwtWith(t, map[string]any{"ttl": 600}), 10 * time.Minute},
		{"ttl_capped_by_exp", jwtWith(t, map[string]any{"ttl": 3600, "exp": now.Add(10 * time.Minute).Unix()}), 10 * time.Minute},
		{"exp_only", jwtWith(t, map[string]any{"exp": now.Add(7 * time.Minute).Unix()}), 7 * time.Minute},
		{"no_claims_default", jwtWith(t, map[string]any{"sub": "https://x"}), dflt},
		{"expired_clamps_to_zero", jwtWith(t, map[string]any{"exp": now.Add(-time.Minute).Unix()}), 0},
		{"ttl_wrong_type_default", jwtWith(t, map[string]any{"ttl": "600"}), dflt},
		{"not_a_jwt_default", []byte("garbage"), dflt},
		{"cwt_form_default", []byte{0xd2, 0x84, 0x43, 0xa1, 0x01, 0x26}, dflt}, // CBOR: no CBOR parsing in services
		{"empty_default", nil, dflt},
		{"bad_base64_payload_default", []byte("eyJhbGciOiJFUzI1NiJ9.!!!.c2ln"), dflt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := prefetch.PeekTTL(tt.token, dflt, now)
			qt.Check(t, qt.Equals(got, tt.want))
		})
	}
}

// Parser of untrusted input — must not panic, ever.
func FuzzPeekTTL(f *testing.F) {
	f.Add([]byte("eyJhbGciOiJFUzI1NiJ9.eyJ0dGwiOjYwMH0.c2ln"))
	f.Add([]byte("a.b.c"))
	f.Add([]byte{0xd2, 0x84})
	f.Add([]byte(""))
	now := time.Unix(1_800_000_000, 0)
	f.Fuzz(func(t *testing.T, data []byte) {
		got := prefetch.PeekTTL(data, 5*time.Minute, now) // must not panic
		if got < 0 {
			t.Fatalf("PeekTTL returned negative duration %v", got)
		}
	})
}
