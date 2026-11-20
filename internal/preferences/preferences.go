// Package preferences protects boolean device settings independently of the
// optional application passphrase. It never contains identity or contact data.
package preferences

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
	"golang.org/x/crypto/chacha20poly1305"
	"whatsup/internal/keymaterial"
)

const Filename = "app-preferences.vault"
const lockFilename = "app-preferences.lock"
const maxFileBytes = 2048

var header = []byte("WUPPREF\x01")

var (
	ErrUnavailable = errors.New("device preferences are unavailable")
	ErrCredential  = errors.New("device preferences require the existing OS credential")
	ErrCorrupt     = errors.New("device preferences could not be authenticated")
	ErrUnsafePath  = errors.New("device preferences require private regular files")
	ErrBusy        = errors.New("device preferences are already open")
)

type Values struct {
	CloseToTray         bool `json:"closeToTray"`
	StartAtLogin        bool `json:"startAtLogin"`
	Notifications       bool `json:"notifications"`
	NotificationName    bool `json:"notificationName"`
	NotificationPreview bool `json:"notificationPreview"`
	// VoiceCalls allows placing and receiving voice calls, which use the
	// microphone. It is off unless the person switches it on.
	VoiceCalls bool `json:"voiceCalls"`
	// CallRingMuted silences the ring sound; an incoming call still shows.
	CallRingMuted bool `json:"callRingMuted"`
	// VideoCalls allows video in calls, which uses the camera. It has no
	// effect unless VoiceCalls is on too, and is off unless switched on.
	VideoCalls bool `json:"videoCalls"`
}

func Defaults() Values { return Values{CloseToTray: true, Notifications: true} }

type Snapshot struct {
	Values         Values `json:"values"`
	TrayNoticeSeen bool   `json:"trayNoticeSeen"`
}

// The stored form keeps the original field set byte for byte. Settings added
// later are written only when switched on, so a file saved before they existed
// still authenticates and canonicalizes unchanged, and a file with every later
// setting off is readable by the earlier build. An earlier build rejects a
// file that has one of them on; switch call settings off before downgrading.
type storedValues struct {
	CloseToTray         bool `json:"closeToTray"`
	StartAtLogin        bool `json:"startAtLogin"`
	Notifications       bool `json:"notifications"`
	NotificationName    bool `json:"notificationName"`
	NotificationPreview bool `json:"notificationPreview"`
	VoiceCalls          bool `json:"voiceCalls,omitempty"`
	CallRingMuted       bool `json:"callRingMuted,omitempty"`
	VideoCalls          bool `json:"videoCalls,omitempty"`
}

type storedSnapshot struct {
	Values         storedValues `json:"values"`
	TrayNoticeSeen bool         `json:"trayNoticeSeen"`
}

type stored struct {
	Version int            `json:"version"`
	State   storedSnapshot `json:"state"`
}

func toStored(state Snapshot) storedSnapshot {
	return storedSnapshot{Values: storedValues(state.Values), TrayNoticeSeen: state.TrayNoticeSeen}
}

func fromStored(state storedSnapshot) Snapshot {
	return Snapshot{Values: Values(state.Values), TrayNoticeSeen: state.TrayNoticeSeen}
}

// KeyStore deliberately has no Set method: preference access cannot create or
// replace the wrapping credential belonging to the identity/message store.
type KeyStore interface{ Get() (string, error) }

type Store struct{ *privateState }
type privateState struct {
	mu          sync.Mutex
	directory   string
	keys        KeyStore
	lock        *flock.Flock
	lockInfo    os.FileInfo
	fileInfo    os.FileInfo
	closed      bool
	failed      bool
	beforeWrite func() error // package-only failure boundary used by tests
}

func (Store) String() string               { return "[private WhatsUp device preferences]" }
func (s Store) GoString() string           { return s.String() }
func (Store) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func regular(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || !owned(info) || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return ErrUnsafePath
	}
	return nil
}

func (s *Store) paths() error {
	info, err := os.Lstat(s.directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !owned(info) || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return ErrUnsafePath
	}
	for name, previous := range map[string]os.FileInfo{Filename: s.fileInfo, lockFilename: s.lockInfo} {
		path := filepath.Join(s.directory, name)
		if err := regular(path); err != nil {
			return err
		}
		if previous != nil {
			current, err := os.Lstat(path)
			if err != nil || !os.SameFile(previous, current) {
				return ErrUnsafePath
			}
		}
	}
	return nil
}

func Open(directory string, keys KeyStore) (_ *Store, result error) {
	if keys == nil || !filepath.IsAbs(directory) {
		return nil, ErrUnsafePath
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, ErrUnsafePath
	}
	s := &Store{privateState: &privateState{directory: directory, keys: keys}}
	defer func() {
		if result != nil {
			_ = s.Close()
		}
	}()
	if err := s.paths(); err != nil {
		return nil, err
	}
	s.lock = flock.New(filepath.Join(directory, lockFilename), flock.SetPermissions(0600))
	ok, err := s.lock.TryLock()
	if err != nil {
		return nil, ErrUnavailable
	}
	if !ok {
		return nil, ErrBusy
	}
	s.lockInfo, err = os.Lstat(filepath.Join(directory, lockFilename))
	if err != nil {
		return nil, ErrUnsafePath
	}
	s.fileInfo, err = os.Lstat(filepath.Join(directory, Filename))
	if err != nil && !os.IsNotExist(err) {
		return nil, ErrUnsafePath
	}
	if _, err := s.Load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) usable() error {
	if s == nil || s.privateState == nil || s.closed || s.failed {
		return ErrUnavailable
	}
	return s.paths()
}

func (s *Store) existingData() bool {
	for _, name := range []string{Filename, "state.vault", "messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal"} {
		if _, err := os.Lstat(filepath.Join(s.directory, name)); !os.IsNotExist(err) {
			return true
		}
	}
	return false
}

func (s *Store) key(required bool) ([]byte, error) {
	encoded, err := s.keys.Get()
	if errors.Is(err, keyring.ErrNotFound) && !required && !s.existingData() {
		return nil, nil
	}
	if err != nil {
		return nil, ErrCredential
	}
	root, err := base64.RawStdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(root) != 32 {
		clear(root)
		return nil, ErrCredential
	}
	defer clear(root)
	key, err := keymaterial.Derive(root, "device-preferences/v1")
	if err != nil {
		return nil, ErrCredential
	}
	return key, nil
}

func (s *Store) Load() (Snapshot, error) {
	if s == nil || s.privateState == nil {
		return Snapshot{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return Snapshot{}, err
	}
	return s.loadLocked()
}

func (s *Store) loadLocked() (Snapshot, error) {
	key, err := s.key(false)
	if err != nil {
		return Snapshot{}, err
	}
	defer clear(key)
	f, err := os.Open(filepath.Join(s.directory, Filename))
	if os.IsNotExist(err) {
		return Snapshot{Values: Defaults()}, nil
	}
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || s.fileInfo != nil && !os.SameFile(s.fileInfo, info) {
		return Snapshot{}, ErrUnsafePath
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}
	if len(key) != 32 || len(data) > maxFileBytes || len(data) < len(header)+24+16 || !bytes.Equal(data[:len(header)], header) {
		return Snapshot{}, ErrCorrupt
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return Snapshot{}, ErrCredential
	}
	plain, err := aead.Open(nil, data[len(header):len(header)+24], data[len(header)+24:], header)
	if err != nil {
		return Snapshot{}, ErrCorrupt
	}
	defer clear(plain)
	var state stored
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || decoder.Decode(new(any)) != io.EOF || state.Version != 1 {
		return Snapshot{}, ErrCorrupt
	}
	canonical, err := json.Marshal(state)
	defer clear(canonical)
	if err != nil || !bytes.Equal(plain, canonical) {
		return Snapshot{}, ErrCorrupt
	}
	return fromStored(state.State), nil
}

func (s *Store) Save(state Snapshot) error {
	if s == nil || s.privateState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usable(); err != nil {
		return err
	}
	if _, err := s.loadLocked(); err != nil {
		return err
	}
	key, err := s.key(true)
	if err != nil {
		return err
	}
	defer clear(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return ErrCredential
	}
	plain, err := json.Marshal(stored{Version: 1, State: toStored(state)})
	if err != nil {
		return ErrUnavailable
	}
	defer clear(plain)
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return ErrUnavailable
	}
	wire := append(bytes.Clone(header), nonce...)
	wire = aead.Seal(wire, nonce, plain, header)
	f, err := os.CreateTemp(s.directory, ".preferences-*.tmp")
	if err != nil {
		return ErrUnavailable
	}
	path := f.Name()
	defer os.Remove(path)
	if f.Chmod(0600) != nil {
		_ = f.Close()
		return ErrUnsafePath
	}
	if _, err := f.Write(wire); err != nil {
		_ = f.Close()
		return ErrUnavailable
	}
	if f.Sync() != nil || f.Close() != nil {
		_ = f.Close()
		return ErrUnavailable
	}
	if s.beforeWrite != nil && s.beforeWrite() != nil {
		return ErrUnavailable
	}
	if err := s.paths(); err != nil {
		return err
	}
	if err := os.Rename(path, filepath.Join(s.directory, Filename)); err != nil {
		return ErrUnavailable
	}
	if err := syncDirectory(s.directory); err != nil {
		s.failed = true
		return ErrUnavailable
	}
	s.fileInfo, err = os.Lstat(filepath.Join(s.directory, Filename))
	if err != nil {
		s.failed = true
		return ErrUnavailable
	}
	// Reopen and authenticate the completed replacement before reporting success.
	verified, err := s.loadLocked()
	if err != nil || verified != state {
		s.failed = true
		return ErrUnavailable
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.privateState == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.lock != nil {
		if s.lock.Close() != nil {
			return ErrUnavailable
		}
		s.lock = nil
	}
	return nil
}
