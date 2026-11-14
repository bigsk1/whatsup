package storage

import (
	"bytes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/crypto/chacha20poly1305"

	"whatsup/internal/keymaterial"
)

const maxRootFileBytes = (MaxStateBytes+256)*4/3 + 4096

var rootHeader = []byte("WUPVAULT\x02")

type sealedRootValue struct {
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// The reserved slot is deliberately unsupported. A non-null value must never
// silently fall back to OS-only unlocking before passphrase mode is implemented.
type reservedPassphraseSlot struct{}
type rootKeySlots struct {
	OS         sealedRootValue         `json:"os"`
	Passphrase *reservedPassphraseSlot `json:"passphrase"`
}
type rootMetadata struct {
	Version int          `json:"version"`
	Mode    string       `json:"mode"`
	Slots   rootKeySlots `json:"slots"`
}
type rootEnvelope struct {
	Version int             `json:"version"`
	Mode    string          `json:"mode"`
	Slots   rootKeySlots    `json:"slots"`
	Payload sealedRootValue `json:"payload"`
}

func messageFilesExist(directory string) bool {
	for _, name := range []string{"messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal"} {
		if _, err := os.Lstat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func (v *Vault) clearMasterLocked() {
	if v == nil || v.vaultState == nil {
		return
	}
	clear(v.master)
	v.master = nil
	v.masterPersisted = false
}

// The caller must hold v.mu. Derivation never creates an unpersisted root key.
func (v *Vault) deriveKeyLocked(purpose string) ([]byte, error) {
	if v == nil || v.vaultState == nil || v.closed {
		return nil, ErrLocked
	}
	if !v.masterPersisted || len(v.master) != keymaterial.KeyBytes {
		return nil, ErrCredential
	}
	return keymaterial.Derive(v.master, purpose)
}

func rootAEAD(key []byte) (cipher.AEAD, error) {
	return chacha20poly1305.NewX(key)
}

func sealRootValue(aead cipher.AEAD, plaintext, aad []byte) (sealedRootValue, error) {
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return sealedRootValue{}, err
	}
	return sealedRootValue{Nonce: nonce, Ciphertext: aead.Seal(nil, nonce, plaintext, aad)}, nil
}

func rootAAD(metadata rootMetadata) ([]byte, error) {
	canonical, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	return append(append([]byte(nil), rootHeader...), canonical...), nil
}

func (v *Vault) sealRootLocked(plaintext []byte) ([]byte, error) {
	if v == nil || v.vaultState == nil || v.closed {
		return nil, ErrLocked
	}
	if len(v.master) != keymaterial.KeyBytes {
		// Open does not decrypt state. A caller saving an existing root directly
		// must load its master first, rather than orphan the SQLite key hierarchy.
		old, err := v.loadLocked()
		clear(old)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if len(v.master) != keymaterial.KeyBytes {
			v.master, err = keymaterial.RandomKey()
			if err != nil {
				return nil, err
			}
		}
	}
	wrapKey, err := keymaterial.Derive(v.key, "os-master-wrap/v2")
	if err != nil {
		return nil, err
	}
	defer clear(wrapKey)
	wrapping, err := rootAEAD(wrapKey)
	if err != nil {
		return nil, err
	}
	slot, err := sealRootValue(wrapping, v.master, append(append([]byte(nil), rootHeader...), []byte("os-only master slot")...))
	if err != nil {
		return nil, err
	}
	metadata := rootMetadata{Version: 2, Mode: "os-only", Slots: rootKeySlots{OS: slot}}
	aad, err := rootAAD(metadata)
	if err != nil {
		return nil, err
	}
	payloadKey, err := keymaterial.Derive(v.master, "vault-envelope/v2")
	if err != nil {
		return nil, err
	}
	defer clear(payloadKey)
	payloadAEAD, err := rootAEAD(payloadKey)
	if err != nil {
		return nil, err
	}
	payload, err := sealRootValue(payloadAEAD, plaintext, aad)
	if err != nil {
		return nil, err
	}
	root := rootEnvelope{Version: metadata.Version, Mode: metadata.Mode, Slots: metadata.Slots, Payload: payload}
	encoded, err := json.Marshal(root)
	if err != nil || len(encoded)+len(rootHeader) > maxRootFileBytes {
		return nil, ErrCorrupt
	}
	return append(append([]byte(nil), rootHeader...), encoded...), nil
}

func (v *Vault) loadRootLocked(encoded []byte) ([]byte, error) {
	if v == nil || v.vaultState == nil || v.closed {
		return nil, ErrLocked
	}
	var root rootEnvelope
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&root) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrCorrupt
	}
	canonical, err := json.Marshal(root)
	if err != nil || !bytes.Equal(canonical, encoded) || root.Version != 2 || root.Mode != "os-only" || root.Slots.Passphrase != nil || len(root.Slots.OS.Nonce) != chacha20poly1305.NonceSizeX || len(root.Slots.OS.Ciphertext) != keymaterial.KeyBytes+chacha20poly1305.Overhead || len(root.Payload.Nonce) != chacha20poly1305.NonceSizeX || len(root.Payload.Ciphertext) < chacha20poly1305.Overhead || len(root.Payload.Ciphertext) > MaxStateBytes+chacha20poly1305.Overhead {
		return nil, ErrCorrupt
	}
	wrapKey, err := keymaterial.Derive(v.key, "os-master-wrap/v2")
	if err != nil {
		return nil, ErrCorrupt
	}
	defer clear(wrapKey)
	wrapping, err := rootAEAD(wrapKey)
	if err != nil {
		return nil, ErrCorrupt
	}
	master, err := wrapping.Open(nil, root.Slots.OS.Nonce, root.Slots.OS.Ciphertext, append(append([]byte(nil), rootHeader...), []byte("os-only master slot")...))
	if err != nil || len(master) != keymaterial.KeyBytes {
		clear(master)
		return nil, ErrCorrupt
	}
	defer clear(master)
	payloadKey, err := keymaterial.Derive(master, "vault-envelope/v2")
	if err != nil {
		return nil, ErrCorrupt
	}
	defer clear(payloadKey)
	payloadAEAD, err := rootAEAD(payloadKey)
	if err != nil {
		return nil, ErrCorrupt
	}
	aad, err := rootAAD(rootMetadata{Version: root.Version, Mode: root.Mode, Slots: root.Slots})
	if err != nil {
		return nil, ErrCorrupt
	}
	plaintext, err := payloadAEAD.Open(nil, root.Payload.Nonce, root.Payload.Ciphertext, aad)
	if err != nil {
		return nil, ErrCorrupt
	}
	v.master = bytes.Clone(master)
	v.masterPersisted = true
	v.legacy = false
	return plaintext, nil
}
