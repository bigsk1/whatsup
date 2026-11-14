// Package keymaterial defines the application's fixed, domain-separated key
// derivation and the RFC 9106 second recommended Argon2id parameter set. It never
// reads files, opens credentials, logs secrets or selects an unlock mode.
package keymaterial

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	KeyBytes                  = 32
	ArgonTime          uint32 = 3
	ArgonMemoryKiB     uint32 = 64 * 1024
	ArgonParallelism   uint8  = 4
	ArgonSaltBytes            = 16
	MaxPassphraseBytes        = 4096
)

var ErrMaterial = errors.New("invalid local key material")

// RandomKey produces a new 256-bit master or wrapping component. Callers own
// the returned buffer and must clear it when its lifetime ends.
func RandomKey() ([]byte, error) {
	key := make([]byte, KeyBytes)
	if _, err := rand.Read(key); err != nil {
		clear(key)
		return nil, err
	}
	return key, nil
}

// Derive separates fixed internal purposes using the Go standard library's
// HKDF-SHA256. Purposes are reviewed identifiers, never peer-supplied strings.
func Derive(root []byte, purpose string) ([]byte, error) {
	if len(root) != KeyBytes || len(purpose) == 0 || len(purpose) > 128 || strings.IndexFunc(purpose, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '/' || r == ':')
	}) >= 0 {
		return nil, ErrMaterial
	}
	return hkdf.Key(sha256.New, root, nil, "WhatsUp key hierarchy v1\x00"+purpose, KeyBytes)
}

func PassphraseSalt() ([]byte, error) {
	salt := make([]byte, ArgonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		clear(salt)
		return nil, err
	}
	return salt, nil
}

// PassphraseKey uses RFC 9106 section 4's second recommended Argon2id option:
// t=3, 64 MiB, p=4, 128-bit salt and 256-bit output. No environment or platform
// fallback can lower those parameters. The caller controls secret lifetime.
func PassphraseKey(passphrase, salt []byte) ([]byte, error) {
	if len(passphrase) == 0 || len(passphrase) > MaxPassphraseBytes || len(salt) != ArgonSaltBytes {
		return nil, ErrMaterial
	}
	return argon2.IDKey(passphrase, salt, ArgonTime, ArgonMemoryKiB, ArgonParallelism, KeyBytes), nil
}
