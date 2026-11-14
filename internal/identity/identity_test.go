package identity

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestIdentityRestorationAndSignatures(t *testing.T) {
	first, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	seed := first.Seed()
	restored, err := FromSeed(seed[:])
	if err != nil {
		t.Fatal(err)
	}
	if first.PublicKey() != restored.PublicKey() {
		t.Fatal("restored identity changed")
	}
	message := []byte("synthetic identity test")
	signature := first.Sign(message)
	if !restored.PublicKey().Verify(message, signature) {
		t.Fatal("valid signature rejected")
	}
	if first.PublicKey().Verify([]byte("changed"), signature) {
		t.Fatal("modified message accepted")
	}
	other, _ := Generate()
	if other.PublicKey().Verify(message, signature) {
		t.Fatal("substituted identity accepted")
	}
	encoded, _ := json.Marshal(first)
	if string(encoded) != "{}" || bytes.Contains(encoded, seed[:]) {
		t.Fatal("private identity material serialized")
	}
}

func TestPublicKeyCanonicalEncoding(t *testing.T) {
	i, _ := Generate()
	key := i.PublicKey()
	parsed, err := ParsePublicKey(key.String())
	if err != nil || parsed != key || !parsed.Equal(key) {
		t.Fatalf("round trip: %v", err)
	}
	if len(key.Fingerprint()) != 64 {
		t.Fatal("incomplete fingerprint")
	}
	for _, text := range []string{"", key.String() + "=", strings.Repeat("A", 43), " " + key.String(), strings.Repeat("x", 4096)} {
		if _, err := ParsePublicKey(text); err == nil {
			t.Fatalf("invalid public key accepted: length %d", len(text))
		}
	}
	if _, err := FromSeed([]byte("short")); err == nil {
		t.Fatal("short seed accepted")
	}
}
