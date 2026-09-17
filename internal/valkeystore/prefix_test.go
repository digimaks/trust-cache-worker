package valkeystore_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-quicktest/qt"

	"github.com/dativa-lv/trust-cache-worker/internal/valkeystore"
	"github.com/gmb-eudi/go-verifier-helpers/trustcache"
)

// Every key the store touches lands under the configured prefix — the
// contract key names stay literal, the prefix is the only thing added.
func TestPrefixAppliesToEveryKey(t *testing.T) {
	mr := miniredis.RunT(t)
	s, err := valkeystore.New(valkeystore.Options{URL: mr.Addr(), KeyPrefix: "verifierdev"})
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(s.Close)
	ctx := context.Background()
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

	qt.Assert(t, qt.IsNil(s.SetBytes(ctx, "trust:x", []byte("v"), time.Minute)))
	qt.Assert(t, qt.IsTrue(mr.Exists("verifierdev:trust:x")))
	qt.Assert(t, qt.IsFalse(mr.Exists("trust:x")))

	got, err := s.Get(ctx, "trust:x")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(string(got), "v"))

	_, exists, err := s.GetTTL(ctx, "trust:x")
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(exists))

	sw := valkeystore.Swap{
		KeyType: trustcache.TypePIDProvider, ETag: "e1", TTL: time.Hour,
		Entries:   []trustcache.AnchorSetEntry{entry("LV", "e1", time.Hour, now, anchorA)},
		Freshness: trustcache.TypeFreshness{SnapshotID: "e1", FetchedAt: now, ValidUntil: now.Add(time.Hour)},
	}
	qt.Assert(t, qt.IsNil(s.SwapAnchorType(ctx, sw)))
	for _, k := range []string{
		trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV"),
		trustcache.TerritoryIndexKey(trustcache.TypePIDProvider),
		trustcache.ETagKey(trustcache.TypePIDProvider),
		trustcache.FreshnessKey(trustcache.TypePIDProvider),
	} {
		qt.Assert(t, qt.IsTrue(mr.Exists("verifierdev:"+k)), qt.Commentf("key %s", k))
		qt.Assert(t, qt.IsFalse(mr.Exists(k)), qt.Commentf("unprefixed %s must not exist", k))
	}

	// A second swap that drops LV must DEL the prefixed key.
	sw.Entries = []trustcache.AnchorSetEntry{entry("EE", "e2", time.Hour, now, anchorC)}
	sw.ETag = "e2"
	qt.Assert(t, qt.IsNil(s.SwapAnchorType(ctx, sw)))
	qt.Assert(t, qt.IsFalse(mr.Exists("verifierdev:"+trustcache.AnchorSetKey(trustcache.TypePIDProvider, "LV"))))
	qt.Assert(t, qt.IsTrue(mr.Exists("verifierdev:"+trustcache.AnchorSetKey(trustcache.TypePIDProvider, "EE"))))

	// Refresh (304 path) extends the prefixed keys, never creates unprefixed ones.
	fr := trustcache.TypeFreshness{SnapshotID: "e2", FetchedAt: now, ValidUntil: now.Add(2 * time.Hour)}
	qt.Assert(t, qt.IsNil(s.RefreshAnchorType(ctx, trustcache.TypePIDProvider, fr, 2*time.Hour)))
	qt.Assert(t, qt.IsFalse(mr.Exists(trustcache.FreshnessKey(trustcache.TypePIDProvider))))

	// The refs ZSET is read and trimmed under the prefix too.
	_, err = mr.ZAdd("verifierdev:"+trustcache.StatusRefsKey, 1, "https://a")
	qt.Assert(t, qt.IsNil(err))
	_, err = mr.ZAdd("verifierdev:"+trustcache.StatusRefsKey, 2, "https://b")
	qt.Assert(t, qt.IsNil(err))
	top, err := s.TopStatusRefs(ctx, 1)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(top, []string{"https://b"}))
	qt.Assert(t, qt.IsNil(s.TrimStatusRefs(ctx, 1)))
	members, err := mr.ZMembers("verifierdev:" + trustcache.StatusRefsKey)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(members, []string{"https://b"}))
}

// The full connection contract in one run: user + password (from the option,
// not the URL), database index from the URL, prefix on the key.
func TestConnectWithUserPasswordDBAndPrefix(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.RequireUserAuth("verifierdev", "pw-from-file")
	s, err := valkeystore.New(valkeystore.Options{
		URL:       "redis://verifierdev@" + mr.Addr() + "/13",
		Password:  "pw-from-file",
		KeyPrefix: "verifierdev",
	})
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(s.Close)

	qt.Assert(t, qt.IsNil(s.SetBytes(context.Background(), "trust:x", []byte("v"), time.Minute)))
	qt.Assert(t, qt.IsTrue(mr.DB(13).Exists("verifierdev:trust:x")))
	qt.Assert(t, qt.IsFalse(mr.DB(0).Exists("verifierdev:trust:x")))

	// A wrong password is refused at the first command, not silently ignored.
	bad, err := valkeystore.New(valkeystore.Options{URL: "redis://verifierdev@" + mr.Addr(), Password: "wrong"})
	if err == nil {
		t.Cleanup(bad.Close)
		err = bad.SetBytes(context.Background(), "trust:y", []byte("v"), time.Minute)
	}
	qt.Assert(t, qt.IsNotNil(err))
}
