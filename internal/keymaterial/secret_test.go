package keymaterial

import (
	"bytes"
	"testing"
)

func TestSecretPolicyCountsExactUTF8Bytes(t *testing.T) {
	for _, secret := range [][]byte{[]byte("123456789012"), []byte("  spaces count  "), bytes.Repeat([]byte("é"), 512)} {
		if ValidateSecret(secret) != nil {
			t.Fatal("valid exact-byte secret rejected")
		}
	}
	for _, secret := range [][]byte{nil, []byte("12345678901"), bytes.Repeat([]byte("é"), 513), append(bytes.Repeat([]byte{'a'}, 12), 0xff)} {
		if ValidateSecret(secret) == nil {
			t.Fatal("invalid secret accepted")
		}
	}
}
