package trustsync

import (
	"fmt"

	trust "github.com/gmb-eudi/go-eudi-trust"

	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

// typeTable is the single bidirectional mapping between go-eudi-trust anchor
// types and the wire/key taxonomy. Exhaustive by construction; tests pin all
// 11 (7 provider/CA/registrar types + 4 *_status types).
var typeTable = []struct {
	t    trust.AnchorType
	wire string
}{
	{trust.PIDProvider, trustcache.TypePIDProvider},
	{trust.QEAAProvider, trustcache.TypeQEAAProvider},
	{trust.PubEAAProvider, trustcache.TypePubEAAProvider},
	{trust.EAAProvider, trustcache.TypeEAAProvider},
	{trust.WalletProvider, trustcache.TypeWalletProvider},
	{trust.AccessCA, trustcache.TypeAccessCA},
	{trust.WRPRCIssuer, trustcache.TypeWRPRCIssuer},
	{trust.PIDProviderStatus, trustcache.TypePIDProviderStatus},
	{trust.QEAAProviderStatus, trustcache.TypeQEAAProviderStatus},
	{trust.PubEAAProviderStatus, trustcache.TypePubEAAProviderStatus},
	{trust.EAAProviderStatus, trustcache.TypeEAAProviderStatus},
}

// WireType maps a trust.AnchorType to its wire/key name. Unknown = error —
// never fall through to a guessed key (fail-closed posture).
func WireType(t trust.AnchorType) (string, error) {
	for _, e := range typeTable {
		if e.t == t {
			return e.wire, nil
		}
	}
	return "", fmt.Errorf("trustsync: unknown anchor type %q", string(t))
}

// ParseTypes maps configured wire names to trust.AnchorTypes; any unknown
// name fails configuration validation (typos must not silently drop a type).
func ParseTypes(wire []string) ([]trust.AnchorType, error) {
	out := make([]trust.AnchorType, 0, len(wire))
	for _, w := range wire {
		found := false
		for _, e := range typeTable {
			if e.wire == w {
				out = append(out, e.t)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("trustsync: unknown anchor type %q in configuration", w)
		}
	}
	return out, nil
}
