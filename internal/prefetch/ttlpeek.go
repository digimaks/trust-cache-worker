package prefetch

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"time"
)

// PeekTTL extracts the `ttl` claim (seconds; the Token Status List draft),
// capped by `exp`, from a JWT-form status list token — WITHOUT verifying it.
// Unverified by design: the value only bounds how long the raw bytes stay in
// the prefetch cache; go-statuslist verifies the token and owns authoritative
// TTL semantics at check time. Non-JWT input (including the CWT/CBOR form —
// services never import a CBOR parser directly) returns dflt. Never panics;
// never returns a negative duration.
func PeekTTL(token []byte, dflt time.Duration, now time.Time) time.Duration {
	parts := bytes.Split(token, []byte("."))
	if len(parts) != 3 {
		return dflt
	}
	payload := make([]byte, base64.RawURLEncoding.DecodedLen(len(parts[1])))
	n, err := base64.RawURLEncoding.Decode(payload, parts[1])
	if err != nil {
		return dflt
	}
	var claims struct {
		TTL *int64 `json:"ttl"`
		Exp *int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload[:n], &claims); err != nil {
		return dflt
	}
	if claims.TTL == nil && claims.Exp == nil {
		return dflt
	}
	out := dflt
	hasTTL := claims.TTL != nil && *claims.TTL > 0
	if hasTTL {
		out = time.Duration(*claims.TTL) * time.Second
	}
	if claims.Exp != nil {
		untilExp := time.Unix(*claims.Exp, 0).Sub(now)
		switch {
		case hasTTL && untilExp < out:
			// Both claims present: exp caps ttl, never extends it.
			out = untilExp
		case !hasTTL:
			// exp is the only signal: it defines the TTL directly (may be
			// larger than dflt), not merely a cap on dflt.
			out = untilExp
		}
	}
	if out < 0 {
		return 0
	}
	return out
}
