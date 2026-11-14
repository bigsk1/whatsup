package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	encoded, err := json.Marshal(first)
	if err == nil || len(encoded) != 0 {
		t.Fatal("private identity was serializable")
	}
}

func TestIdentityPointerAndValueFormattingCannotExposeSyntheticSeed(t *testing.T) {
	i, err := FromSeed(bytes.Repeat([]byte{123}, 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{i, *i, Identity{}, &Identity{}} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if got := fmt.Sprintf(verb, value); got != "[private WhatsUp identity]" {
				t.Fatal("identity formatting exposed a private seed")
			}
		}
		if encoded, err := json.Marshal(value); err == nil || len(encoded) != 0 {
			t.Fatal("identity value or pointer was serializable")
		}
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
