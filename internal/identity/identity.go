// Package identity owns Ed25519 identity material and canonical public identifiers.
// Private material is deliberately absent from JSON and frontend DTOs. The caller
// must persist seeds only through the encrypted, OS-protected backend store.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
)

var ErrInvalidKey = errors.New("invalid identity key")

var errPrivateSerialization = errors.New("private identity cannot be serialized")

// PublicKey is the public half of a device identity, never a display name.
type PublicKey [ed25519.PublicKeySize]byte

// Identity keeps its private seed out of reflection-based serializers.
type Identity struct {
	seed [ed25519.SeedSize]byte
}

func (Identity) String() string               { return "[private WhatsUp identity]" }
func (i Identity) GoString() string           { return i.String() }
func (Identity) MarshalJSON() ([]byte, error) { return nil, errPrivateSerialization }

func Generate() (*Identity, error) {
	var seed [ed25519.SeedSize]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	return FromSeed(seed[:])
}

// FromSeed restores backend identity material from an already decrypted store.
func FromSeed(seed []byte) (*Identity, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, ErrInvalidKey
	}
	i := &Identity{}
	copy(i.seed[:], seed)
	return i, nil
}

// Seed returns a copy for encrypted backend persistence. Never pass it to a UI,
// invitation, log, or plaintext file.
func (i *Identity) Seed() [ed25519.SeedSize]byte { return i.seed }

func (i *Identity) PublicKey() PublicKey {
	private := ed25519.NewKeyFromSeed(i.seed[:])
	defer clear(private)
	var public PublicKey
	copy(public[:], private[ed25519.SeedSize:])
	return public
}

// Sign is a standard Ed25519 signature primitive, not a transport handshake.
// Higher layers must define authenticated session binding and replay protection.
func (i *Identity) Sign(message []byte) []byte {
	private := ed25519.NewKeyFromSeed(i.seed[:])
	defer clear(private)
	return ed25519.Sign(private, message)
}

func (p PublicKey) Verify(message, signature []byte) bool {
	return p.Valid() && len(signature) == ed25519.SignatureSize && ed25519.Verify(ed25519.PublicKey(p[:]), message, signature)
}

func (p PublicKey) Valid() bool {
	var zero PublicKey
	return subtle.ConstantTimeCompare(p[:], zero[:]) != 1
}

func (p PublicKey) Equal(other PublicKey) bool {
	return subtle.ConstantTimeCompare(p[:], other[:]) == 1
}

func (p PublicKey) String() string { return base64.RawURLEncoding.EncodeToString(p[:]) }

// Fingerprint hashes the complete public key; matching display names are never
// accepted as evidence that two keys belong to the same person.
func (p PublicKey) Fingerprint() string {
	hash := sha256.Sum256(p[:])
	return hex.EncodeToString(hash[:])
}

func ParsePublicKey(text string) (PublicKey, error) {
	var key PublicKey
	if len(text) != base64.RawURLEncoding.EncodedLen(len(key)) {
		return key, ErrInvalidKey
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(text)
	if err != nil || len(decoded) != len(key) {
		return key, ErrInvalidKey
	}
	copy(key[:], decoded)
	if !key.Valid() || key.String() != text {
		return PublicKey{}, ErrInvalidKey
	}
	return key, nil
}

func (p PublicKey) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

func (p *PublicKey) UnmarshalText(text []byte) error {
	key, err := ParsePublicKey(string(text))
	if err != nil {
		return err
	}
	*p = key
	return nil
}
