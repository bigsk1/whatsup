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

// The passphrase layer is itself inside the authenticated OS wrapper. Neither
// factor has an independent route to the master in protected mode.
type reservedPassphraseSlot struct {
	Version int    `json:"version"`
	KDF     string `json:"kdf"`
	Salt    []byte `json:"salt"`
	Nonce   []byte `json:"nonce"`
}
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
	Version       int              `json:"version"`
	Mode          string           `json:"mode"`
	Slots         rootKeySlots     `json:"slots"`
	Payload       sealedRootValue  `json:"payload"`
	ManagedMaster *sealedRootValue `json:"managedMaster,omitempty"`
}

func messageFilesExist(directory string) bool {
	for _, name := range []string{"messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal"} {
		if _, err := os.Lstat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			return true
		}
	}
	path := filepath.Join(directory, "attachments")
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return true
		}
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) > 0 {
			return true
		}
	} else if !os.IsNotExist(err) {
		return true
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
	mode := v.protectionMode
	if mode == "" {
		mode = "os-only"
	}
	wrapped := v.master
	var profile *reservedPassphraseSlot
	osAAD := append(append([]byte(nil), rootHeader...), []byte("os-only master slot")...)
	if mode == "os-plus-passphrase" {
		if len(v.passKey) != 32 || len(v.passSalt) != 16 {
			return nil, ErrPassphraseRequired
		}
		profile = &reservedPassphraseSlot{Version: 1, KDF: "argon2id-rfc9106-second", Salt: bytes.Clone(v.passSalt), Nonce: make([]byte, 24)}
		if _, err := rand.Read(profile.Nonce); err != nil {
			return nil, err
		}
		passAEAD, err := rootAEAD(v.passKey)
		if err != nil {
			return nil, err
		}
		innerAAD := passphraseAAD(profile, "master")
		wrapped = passAEAD.Seal(nil, profile.Nonce, v.master, innerAAD)
		defer clear(wrapped)
		osAAD = passphraseAAD(profile, "os-layer")
	} else if mode != "os-only" {
		return nil, ErrCorrupt
	}
	slot, err := sealRootValue(wrapping, wrapped, osAAD)
	if err != nil {
		return nil, err
	}
	metadata := rootMetadata{Version: 2, Mode: mode, Slots: rootKeySlots{OS: slot, Passphrase: profile}}
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

func decodeRoot(encoded []byte) (root rootEnvelope, result error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&root) != nil || decoder.Decode(new(any)) != io.EOF {
		return root, ErrCorrupt
	}
	canonical, err := json.Marshal(root)
	if err != nil || !bytes.Equal(canonical, encoded) || root.Version != 2 || len(root.Slots.OS.Nonce) != chacha20poly1305.NonceSizeX || len(root.Payload.Nonce) != chacha20poly1305.NonceSizeX || len(root.Payload.Ciphertext) < chacha20poly1305.Overhead || len(root.Payload.Ciphertext) > MaxStateBytes+chacha20poly1305.Overhead {
		return root, ErrCorrupt
	}
	if root.Mode == "os-only" {
		if root.Slots.Passphrase != nil || len(root.Slots.OS.Ciphertext) != 48 {
			return root, ErrCorrupt
		}
	} else if root.Mode == "os-plus-passphrase" {
		p := root.Slots.Passphrase
		if p == nil || p.Version != 1 || p.KDF != "argon2id-rfc9106-second" || len(p.Salt) != 16 || len(p.Nonce) != 24 || len(root.Slots.OS.Ciphertext) != 64 {
			return root, ErrCorrupt
		}
	} else {
		return root, ErrCorrupt
	}
	if root.ManagedMaster != nil && (len(root.ManagedMaster.Nonce) != 24 || len(root.ManagedMaster.Ciphertext) != 48) {
		return root, ErrCorrupt
	}
	return root, nil
}

func passphraseAAD(p *reservedPassphraseSlot, purpose string) []byte {
	encoded, _ := json.Marshal(p)
	result := append(append([]byte(nil), rootHeader...), []byte("os-plus-passphrase "+purpose+"\x00")...)
	return append(result, encoded...)
}

func (v *Vault) loadRootLocked(encoded []byte) ([]byte, error) {
	if v == nil || v.vaultState == nil || v.closed {
		return nil, ErrLocked
	}
	root, err := decodeRoot(encoded)
	if err != nil {
		return nil, err
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
	osAAD := append(append([]byte(nil), rootHeader...), []byte("os-only master slot")...)
	if root.Mode == "os-plus-passphrase" {
		osAAD = passphraseAAD(root.Slots.Passphrase, "os-layer")
	}
	master, err := wrapping.Open(nil, root.Slots.OS.Nonce, root.Slots.OS.Ciphertext, osAAD)
	if err != nil {
		clear(master)
		return nil, ErrCorrupt
	}
	if root.Mode == "os-plus-passphrase" {
		if len(v.passKey) != 32 || !bytes.Equal(v.passSalt, root.Slots.Passphrase.Salt) {
			clear(master)
			return nil, ErrPassphraseRequired
		}
		passAEAD, err := rootAEAD(v.passKey)
		if err != nil {
			clear(master)
			return nil, ErrPassphrase
		}
		inner := master
		master, err = passAEAD.Open(nil, root.Slots.Passphrase.Nonce, inner, passphraseAAD(root.Slots.Passphrase, "master"))
		clear(inner)
		if err != nil {
			clear(master)
			return nil, ErrPassphrase
		}
	}
	if len(master) != 32 {
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
	v.protectionMode = root.Mode
	return plaintext, nil
}
