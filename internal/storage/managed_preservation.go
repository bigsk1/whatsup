package storage

import (
	"bytes"
	"crypto/rand"
	"path/filepath"

	"golang.org/x/crypto/chacha20poly1305"
	"whatsup/internal/keymaterial"
)

var preservedRootHeader = []byte("WUPPRESERVE\x01")

// Opaque historical roots can retain their own key slots. Preserve their exact
// bytes behind the current master, never behind the OS credential alone. These
// envelopes are historical material only and cannot be loaded as a live root.
// A basename binds the root role while allowing recovery's same-name move into
// its rollback directory. The profile master supplies the profile boundary.
const maxManagedRootBytes = maxRootFileBytes + (1 << 20)

func preservedRootAAD(path string) []byte {
	return append(bytes.Clone(preservedRootHeader), []byte(filepath.Base(path))...)
}

func sealPreservedRoot(v *Vault, path string, data []byte) ([]byte, error) {
	if len(v.master) != 32 || len(data)+len(preservedRootHeader)+24+16 > maxManagedRootBytes {
		return nil, ErrState
	}
	key, err := keymaterial.Derive(v.master, "managed-root-preservation/v1")
	if err != nil {
		return nil, err
	}
	defer clear(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	result := append(bytes.Clone(preservedRootHeader), nonce...)
	return aead.Seal(result, nonce, data, preservedRootAAD(path)), nil
}

func openPreservedRoot(v *Vault, path string, data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, preservedRootHeader) || len(data) < len(preservedRootHeader)+24+16 || len(data) > maxManagedRootBytes || len(v.master) != 32 {
		return nil, ErrCorrupt
	}
	key, err := keymaterial.Derive(v.master, "managed-root-preservation/v1")
	if err != nil {
		return nil, err
	}
	defer clear(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	start := len(preservedRootHeader)
	plain, err := aead.Open(nil, data[start:start+aead.NonceSize()], data[start+aead.NonceSize():], preservedRootAAD(path))
	if err != nil {
		return nil, ErrCorrupt
	}
	return plain, nil
}

type managedCopyPolicy struct {
	preserveOpaque bool
	activeRoot     string
}

func preserveManagedRoot(v *Vault, path string, data []byte, operations managedCopyIO, policy managedCopyPolicy, cause error) error {
	if !policy.preserveOpaque || path == policy.activeRoot {
		return managedCopyError(v, path, cause)
	}
	sealed, err := sealPreservedRoot(v, path, data)
	if err != nil {
		return managedCopyError(v, path, err)
	}
	if err := replaceManagedRootTracked(v, path, sealed, operations); err != nil {
		return managedCopyError(v, path, err)
	}
	installed, err := readCipherFile(path, maxManagedRootBytes)
	if err != nil {
		return managedCopyError(v, path, err)
	}
	plain, err := openPreservedRoot(v, path, installed)
	defer clear(plain)
	if err != nil || !bytes.Equal(plain, data) {
		return managedCopyError(v, path, ErrCorrupt)
	}
	return nil
}

// Only a confirmed, journal-authenticated recovery replacement may preserve an
// unreadable historical root automatically. Ordinary opens and mode changes
// continue to reject damaged or unauthenticated preserved material.
func protectRecoveryCopiesLocked(v *Vault, activeRoot string) error {
	return protectManagedCopiesWithPolicyLocked(v, managedCopyIO{}, managedCopyPolicy{preserveOpaque: true, activeRoot: activeRoot})
}
