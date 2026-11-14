// Package diagnostics records bounded, local troubleshooting metadata. It has no
// network client or upload path. Only finite event codes and fixed runtime/build
// fields may be recorded; arbitrary log messages and attributes are never stored.
package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const (
	MaxFileBytes     = 256 << 10
	MaxRetainedBytes = 2 * MaxFileBytes
	currentFilename  = "diagnostics.jsonl"
	previousFilename = "diagnostics.previous.jsonl"
	lockFilename     = "diagnostics.lock"
)

// Event is deliberately an enumeration, not a string supplied by an error, peer,
// bridge, or log message. Zero and unknown values are rejected.
type Event uint8

const (
	Startup Event = iota + 1
	Shutdown
	StorageUnlocked
	StorageLocked
	StorageUnavailable
	RuntimeWarning
	RuntimeError
	RuntimePanic
)

var (
	ErrEvent       = errors.New("unknown local diagnostic event")
	ErrVersion     = errors.New("invalid local diagnostic application version")
	ErrUnsafePath  = errors.New("local diagnostics require private regular files and directory")
	ErrUnavailable = errors.New("local diagnostics could not be opened")
	ErrBusy        = errors.New("local diagnostics are already open in another process")
	ErrWrite       = errors.New("local diagnostics could not be persisted")
	ErrClosed      = errors.New("local diagnostics are closed")
	// Versions are public build identifiers. Restrict prerelease labels too so a
	// caller cannot smuggle a user name, endpoint, or arbitrary text into records.
	versionPattern = regexp.MustCompile(`^[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}(?:-(?:dev|alpha|beta|rc)(?:\.[0-9]{1,6})?)?$`)
	goPattern      = regexp.MustCompile(`^go[0-9]{1,3}\.[0-9]{1,3}(?:\.[0-9]{1,3})?(?:(?:rc|beta)[0-9]{1,3})?$`)
)

func eventName(event Event) (string, error) {
	switch event {
	case Startup:
		return "startup", nil
	case Shutdown:
		return "shutdown", nil
	case StorageUnlocked:
		return "storage_unlocked", nil
	case StorageLocked:
		return "storage_locked", nil
	case StorageUnavailable:
		return "storage_unavailable", nil
	case RuntimeWarning:
		return "runtime_warning", nil
	case RuntimeError:
		return "runtime_error", nil
	case RuntimePanic:
		return "runtime_panic", nil
	default:
		return "", ErrEvent
	}
}

type record struct {
	UTC        time.Time `json:"utc"`
	AppVersion string    `json:"appVersion"`
	GoVersion  string    `json:"goVersion"`
	Platform   string    `json:"platform"`
	Arch       string    `json:"arch"`
	Event      string    `json:"event"`
}

// Recorder owns only the three fixed filenames in its private directory. Parent
// directories and the local OS account remain trusted. Windows files inherit the
// user's application-data ACL; Unix mode bits are not a Windows ACL sandbox.
type Recorder struct {
	mu        sync.Mutex
	directory string
	version   string
	goVersion string
	file      *os.File
	lock      *flock.Flock
	size      int64
	closed    bool
	failed    bool
}

func (r *Recorder) String() string   { return "[local WhatsUp diagnostics recorder]" }
func (r *Recorder) GoString() string { return r.String() }

// Open never creates a remote fallback or returns raw filesystem errors, which
// may contain user paths. An unsafe existing file is preserved and rejected.
func Open(directory, version string) (*Recorder, error) {
	if !versionPattern.MatchString(version) {
		return nil, ErrVersion
	}
	if !filepath.IsAbs(directory) {
		return nil, ErrUnsafePath
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, ErrUnavailable
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !privateMode(info) {
		return nil, ErrUnsafePath
	}
	for _, filename := range []string{currentFilename, previousFilename} {
		if err := regular(filepath.Join(directory, filename), MaxFileBytes); err != nil {
			return nil, err
		}
	}
	if err := regular(filepath.Join(directory, lockFilename), 0); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(directory, lockFilename), flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()
		return nil, ErrUnavailable
	}
	if !ok {
		_ = lock.Close()
		return nil, ErrBusy
	}
	fail := func(err error) (*Recorder, error) { _ = lock.Close(); return nil, err }
	// Recheck after acquiring the writer lock because the previous writer may
	// have rotated between the initial checks and lock acquisition.
	for _, filename := range []string{currentFilename, previousFilename} {
		if err := regular(filepath.Join(directory, filename), MaxFileBytes); err != nil {
			return fail(err)
		}
	}
	// Windows append-only handles cannot truncate a partial final record. Open
	// read/write for bounded tail recovery; the process lock and recorder mutex
	// serialize all writes, and the cursor is explicitly positioned at the end.
	file, err := os.OpenFile(filepath.Join(directory, currentFilename), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fail(ErrUnavailable)
	}
	size, err := recoverTail(file)
	if err != nil {
		_ = file.Close()
		return fail(ErrUnavailable)
	}
	if _, err := file.Seek(size, io.SeekStart); err != nil {
		_ = file.Close()
		return fail(ErrUnavailable)
	}
	goVersion := runtime.Version()
	if !goPattern.MatchString(goVersion) {
		// Development toolchains may contain arbitrary build details. Retain no
		// unreviewed string from them; supported release toolchains match above.
		goVersion = "unknown"
	}
	return &Recorder{directory: directory, version: version, goVersion: goVersion, file: file, lock: lock, size: size}, nil
}

func privateMode(info os.FileInfo) bool {
	return runtime.GOOS == "windows" || info.Mode().Perm()&0077 == 0
}

func regular(filename string, maximum int64) error {
	info, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	if !info.Mode().IsRegular() || !privateMode(info) || info.Size() > maximum {
		return ErrUnsafePath
	}
	return nil
}

// A crash can interrupt one final append. Recover only the incomplete trailing
// line, bounded by MaxFileBytes; never interpret it as a complete diagnostic.
func recoverTail(file *os.File) (int64, error) {
	info, err := file.Stat()
	if err != nil || info.Size() > MaxFileBytes {
		return 0, ErrUnsafePath
	}
	if info.Size() == 0 {
		return 0, nil
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil || len(data) > MaxFileBytes {
		return 0, ErrUnavailable
	}
	if len(data) == 0 {
		return 0, nil
	}
	if data[len(data)-1] == '\n' {
		return int64(len(data)), nil
	}
	length := int64(bytes.LastIndexByte(data, '\n') + 1)
	if err := file.Truncate(length); err != nil {
		return 0, err
	}
	if err := file.Sync(); err != nil {
		return 0, err
	}
	return length, nil
}

// Record persists a finite event with UTC/build/runtime fields only. It accepts
// no text, attributes, errors, identities, filenames, or payload parameters.
func (r *Recorder) Record(event Event) error {
	name, err := eventName(event)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if r.failed || r.file == nil {
		return ErrWrite
	}
	data, err := json.Marshal(record{UTC: time.Now().UTC(), AppVersion: r.version, GoVersion: r.goVersion, Platform: runtime.GOOS, Arch: runtime.GOARCH, Event: name})
	if err != nil {
		return ErrWrite
	}
	data = append(data, '\n')
	if r.size+int64(len(data)) > MaxFileBytes {
		if err := r.rotateLocked(); err != nil {
			r.failed = true
			return err
		}
	}
	if written, err := r.file.Write(data); err != nil || written != len(data) {
		r.failed = true
		return ErrWrite
	}
	r.size += int64(len(data))
	if err := r.file.Sync(); err != nil {
		r.failed = true
		return ErrWrite
	}
	return nil
}

func (r *Recorder) rotateLocked() error {
	for _, filename := range []string{currentFilename, previousFilename} {
		if err := regular(filepath.Join(r.directory, filename), MaxFileBytes); err != nil {
			return err
		}
	}
	file := r.file
	r.file = nil
	if err := file.Close(); err != nil {
		return ErrWrite
	}
	if err := os.Rename(filepath.Join(r.directory, currentFilename), filepath.Join(r.directory, previousFilename)); err != nil {
		return ErrWrite
	}
	file, err := os.OpenFile(filepath.Join(r.directory, currentFilename), os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return ErrWrite
	}
	r.file = file
	r.size = 0
	return nil
}

// Close releases both file and writer lock even after a write failure. If the OS
// reports a lock-release failure, retain that handle so another Close can retry.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	var result error
	if r.file != nil {
		if err := r.file.Close(); err != nil {
			result = ErrWrite
		}
		r.file = nil
	}
	if r.lock != nil {
		if err := r.lock.Close(); err != nil {
			result = ErrWrite
		} else {
			r.lock = nil
		}
	}
	return result
}

// Handler discards raw slog messages and every attribute/group. It records only
// generic warning/error events and never resolves a caller's slog.LogValuer.
func (r *Recorder) Handler() slog.Handler { return &handler{recorder: r} }

func (r *Recorder) WailsLogger() *slog.Logger { return slog.New(r.Handler()) }
