package keymaterial

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func TestHKDFPurposeSeparationAndIndependentKnownAnswer(t *testing.T) {
	root := make([]byte, KeyBytes)
	for index := range root {
		root[index] = byte(index)
	}
	key, err := Derive(root, "test-purpose/v1")
	if err != nil {
		t.Fatal(err)
	}
	// Independently calculated with .NET HMACSHA256's RFC 5869 extract/expand
	// operations, using a zero SHA-256 salt and this exact application label.
	if hex.EncodeToString(key) != "16cd938745f1244b4aaedca17d3c3d509d27f607aae9d5831b6b55d60284278a" {
		t.Fatal("HKDF format or purpose binding changed")
	}
	other, err := Derive(root, "other-purpose/v1")
	if err != nil || bytes.Equal(key, other) {
		t.Fatal("key purposes are not separated")
	}
	root[0] ^= 1
	changed, err := Derive(root, "test-purpose/v1")
	if err != nil || bytes.Equal(key, changed) {
		t.Fatal("key derivation ignored root material")
	}
	for _, purpose := range []string{"", "unexpected space", "UPPER", "user@example.test", string([]byte{'a', 0, 'b'})} {
		if _, err := Derive(root, purpose); !errors.Is(err, ErrMaterial) {
			t.Fatal("unreviewed key purpose was accepted")
		}
	}
	if _, err := Derive(root[:31], "test-purpose/v1"); !errors.Is(err, ErrMaterial) {
		t.Fatal("short root key was accepted")
	}
}

func TestRandomMasterKeysAndPassphraseSalts(t *testing.T) {
	a, err := RandomKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := RandomKey()
	if err != nil || len(a) != 32 || len(b) != 32 || bytes.Equal(a, b) || bytes.Equal(a, make([]byte, 32)) {
		t.Fatal("master keys were not independent random 256-bit values")
	}
	salt, err := PassphraseSalt()
	if err != nil || len(salt) != 16 {
		t.Fatal("Argon2 salt generation failed")
	}
	clear(a)
	clear(b)
	clear(salt)
}

func TestArgon2idUsesFixedRFC9106SecondRecommendation(t *testing.T) {
	if ArgonTime != 3 || ArgonMemoryKiB != 64*1024 || ArgonParallelism != 4 || ArgonSaltBytes != 16 || KeyBytes != 32 {
		t.Fatal("RFC 9106 second recommended parameter set changed")
	}
	salt := []byte("0123456789abcdef")
	password := []byte("synthetic passphrase for a disposable test")
	a, err := PassphraseKey(password, salt)
	if err != nil || len(a) != 32 {
		t.Fatal("Argon2id derivation failed")
	}
	b, err := PassphraseKey(password, salt)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("Argon2id derivation is not deterministic for exact inputs")
	}
	password[0] ^= 1
	c, err := PassphraseKey(password, salt)
	if err != nil || bytes.Equal(a, c) {
		t.Fatal("passphrase did not affect the derived key")
	}
	salt[0] ^= 1
	d, err := PassphraseKey(password, salt)
	if err != nil || bytes.Equal(c, d) {
		t.Fatal("salt did not affect the derived key")
	}
	for _, invalid := range []struct{ password, salt []byte }{
		{nil, salt}, {password, nil}, {password, salt[:15]}, {make([]byte, MaxPassphraseBytes+1), salt},
	} {
		if _, err := PassphraseKey(invalid.password, invalid.salt); !errors.Is(err, ErrMaterial) {
			t.Fatal("invalid passphrase inputs reached the expensive derivation")
		}
	}
	clear(a)
	clear(b)
	clear(c)
	clear(d)
}

// Run with -run=^$ -bench=Argon2idRFC9106Second -benchtime=3x -benchmem.
// This test binary can also be cross-compiled and run on the Ubuntu desktop VM
// without installing Go there. It contains synthetic inputs only.
func BenchmarkArgon2idRFC9106Second(b *testing.B) {
	password := []byte("synthetic benchmark passphrase")
	salt := []byte("0123456789abcdef")
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		key, err := PassphraseKey(password, salt)
		if err != nil {
			b.Fatal(err)
		}
		clear(key)
	}
	b.StopTimer()
	b.ReportMetric(float64(ArgonTime), "passes")
	b.ReportMetric(float64(ArgonMemoryKiB), "memory-KiB")
	b.ReportMetric(float64(ArgonParallelism), "lanes")
}
