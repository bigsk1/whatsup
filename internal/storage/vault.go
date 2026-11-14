// Package storage protects a bounded local state snapshot with AES-256-GCM.
// The wrapping key lives in the OS credential store, never beside the snapshot.
package storage

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
)

const MaxStateBytes = 4 << 20

var (
	ErrLocked     = errors.New("local state is locked")
	ErrBusy       = errors.New("local state is already open in another process")
	ErrCredential = errors.New("OS credential protection is unavailable; local state remains locked")
	ErrCorrupt    = errors.New("local state could not be authenticated")
	ErrUnsafePath = errors.New("local state requires a private directory and regular files")
	header        = []byte("WUPVAULT\x01")
)

// KeyStore is backend-only. Test implementations must never be used by the app.
type KeyStore interface {
	Get() (string, error)
	Set(string) error
}

// OSKeyStore uses Windows Credential Manager or Linux Secret Service. There is
// deliberately no file, environment variable, or plaintext fallback.
type OSKeyStore struct{}

func (OSKeyStore) Get() (string, error)   { return keyring.Get("WhatsUp", "local-state-v1") }
func (OSKeyStore) Set(value string) error { return keyring.Set("WhatsUp", "local-state-v1", value) }

type Vault struct {
	mu          sync.Mutex
	directory   string
	key         []byte
	lock        *flock.Flock
	closed      bool
	application *ApplicationState
}

func (v *Vault) String() string   { return "[locked backend-only WhatsUp vault]" }
func (v *Vault) GoString() string { return v.String() }

// DefaultDirectory keeps private state outside the binary and source tree.
func DefaultDirectory() (string, error) {
	if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" || !filepath.IsAbs(base) {
			return "", ErrUnsafePath
		}
		return filepath.Join(base, "WhatsUp"), nil
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	if !filepath.IsAbs(base) {
		return "", ErrUnsafePath
	}
	return filepath.Join(base, "whatsup"), nil
}

func regular(filename string) error {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrUnsafePath
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return ErrUnsafePath
	}
	return nil
}

func Open(directory string, keys KeyStore) (*Vault, error) {
	if keys == nil || !filepath.IsAbs(directory) {
		return nil, ErrUnsafePath
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, ErrUnsafePath
	}
	statePath := filepath.Join(directory, "state.vault")
	lockPath := filepath.Join(directory, "state.lock")
	if err := regular(statePath); err != nil {
		return nil, err
	}
	if err := regular(lockPath); err != nil {
		return nil, err
	}
	lock := flock.New(lockPath, flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBusy
	}
	fail := func(err error) (*Vault, error) { _ = lock.Close(); return nil, err }
	encoded, err := keys.Get()
	if errors.Is(err, keyring.ErrNotFound) {
		// A missing credential must never silently replace the key of an existing vault.
		if _, statErr := os.Lstat(statePath); !os.IsNotExist(statErr) {
			return fail(ErrCredential)
		}
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return fail(err)
		}
		encoded = base64.RawStdEncoding.EncodeToString(key)
		clear(key)
		if err := keys.Set(encoded); err != nil {
			return fail(ErrCredential)
		}
		// Read back through the real provider before trusting newly persisted material.
		stored, err := keys.Get()
		if err != nil || stored != encoded {
			return fail(ErrCredential)
		}
	} else if err != nil {
		return fail(ErrCredential)
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(key) != 32 {
		clear(key)
		return fail(ErrCredential)
	}
	return &Vault{directory: directory, key: key, lock: lock}, nil
}

func (v *Vault) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(v.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Load authenticates the entire bounded snapshot before returning any plaintext.
func (v *Vault) Load() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil, ErrLocked
	}
	if v.application != nil {
		return nil, ErrScope
	}
	return v.loadLocked()
}

func (v *Vault) loadLocked() ([]byte, error) {
	if v.closed {
		return nil, ErrLocked
	}
	filename := filepath.Join(v.directory, "state.vault")
	if err := regular(filename); err != nil {
		return nil, err
	}
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxStateBytes+128))
	if err != nil {
		return nil, err
	}
	aead, err := v.aead()
	if err != nil {
		return nil, err
	}
	if len(data) > MaxStateBytes+len(header)+aead.NonceSize()+aead.Overhead() || len(data) < len(header)+aead.NonceSize()+aead.Overhead() || !bytes.Equal(data[:len(header)], header) {
		return nil, ErrCorrupt
	}
	nonce := data[len(header) : len(header)+aead.NonceSize()]
	plain, err := aead.Open(nil, nonce, data[len(header)+aead.NonceSize():], header)
	if err != nil {
		return nil, ErrCorrupt
	}
	return plain, nil
}

// Save syncs a ciphertext-only temporary file and atomically replaces the snapshot.
// It provides no rollback protection against a malicious local backup or OS owner.
func (v *Vault) Save(plain []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return ErrLocked
	}
	if v.application != nil {
		return ErrScope
	}
	return v.saveLocked(plain)
}

func (v *Vault) saveLocked(plain []byte) error {
	if v.closed {
		return ErrLocked
	}
	if len(plain) > MaxStateBytes {
		return errors.New("local state exceeds its safety limit")
	}
	aead, err := v.aead()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	sealed := append(append([]byte{}, header...), nonce...)
	sealed = aead.Seal(sealed, nonce, plain, header)
	filename := filepath.Join(v.directory, "state.vault")
	if err := regular(filename); err != nil {
		return err
	}
	f, err := os.CreateTemp(v.directory, ".state-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(sealed); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, filename); err != nil {
		return err
	}
	return syncDirectory(v.directory)
}

func (v *Vault) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed && v.lock == nil {
		return nil
	}
	v.closed = true
	if v.application != nil {
		v.application.clearLocked()
	}
	clear(v.key)
	v.key = nil
	// If the OS refuses unlock, keep the handle so callers can retry. Access to
	// plaintext stays closed regardless of the lock release result.
	err := v.lock.Close()
	if err == nil {
		v.lock = nil
	}
	return err
}
