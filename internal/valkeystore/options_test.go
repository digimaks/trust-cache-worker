package valkeystore

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestClientOptionBareHostPortIsPlainRedis(t *testing.T) {
	copt, err := clientOption(Options{URL: "valkey:6379"})
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(copt.InitAddress, []string{"valkey:6379"}))
	qt.Assert(t, qt.IsNil(copt.TLSConfig))
	qt.Assert(t, qt.Equals(copt.SelectDB, 0))
	qt.Assert(t, qt.IsTrue(copt.ForceSingleClient))
	qt.Assert(t, qt.IsTrue(copt.DisableCache))
}

func TestClientOptionTLSURLCarriesUserDBAndSkipVerify(t *testing.T) {
	copt, err := clientOption(Options{URL: "rediss://verifierdev@cache.example:6379/13?skip_verify=true"})
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.DeepEquals(copt.InitAddress, []string{"cache.example:6379"}))
	qt.Assert(t, qt.Equals(copt.Username, "verifierdev"))
	qt.Assert(t, qt.Equals(copt.Password, ""))
	qt.Assert(t, qt.Equals(copt.SelectDB, 13))
	qt.Assert(t, qt.IsNotNil(copt.TLSConfig))
	qt.Assert(t, qt.IsTrue(copt.TLSConfig.InsecureSkipVerify))
	qt.Assert(t, qt.IsTrue(copt.ForceSingleClient))
	qt.Assert(t, qt.IsTrue(copt.DisableCache))
}

func TestClientOptionPasswordOptionWinsOverURL(t *testing.T) {
	copt, err := clientOption(Options{URL: "redis://u:fromurl@h:6379", Password: "fromfile"})
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(copt.Username, "u"))
	qt.Assert(t, qt.Equals(copt.Password, "fromfile"))

	copt, err = clientOption(Options{URL: "redis://u:fromurl@h:6379"})
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(copt.Password, "fromurl"))
}

func TestClientOptionRejectsEmptyAndUnknownScheme(t *testing.T) {
	_, err := clientOption(Options{URL: ""})
	qt.Assert(t, qt.ErrorIs(err, ErrEmptyURL))
	_, err = clientOption(Options{URL: "http://h:6379"})
	qt.Assert(t, qt.IsNotNil(err))
}

func TestKeyPrefixNormalization(t *testing.T) {
	qt.Assert(t, qt.Equals(keyPrefix(""), ""))
	qt.Assert(t, qt.Equals(keyPrefix("  "), ""))
	qt.Assert(t, qt.Equals(keyPrefix("verifierdev"), "verifierdev:"))
	qt.Assert(t, qt.Equals(keyPrefix("verifierdev:"), "verifierdev:"))
	qt.Assert(t, qt.Equals(keyPrefix("a:b"), "a:b:"))
}
