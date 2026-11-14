package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/zalando/go-keyring"
	"whatsup/internal/keymaterial"
	"whatsup/internal/recovery"
)

var ErrRecovery = errors.New("recovery data could not be authenticated or installed")
var ErrRecoveryReplace = errors.New("explicit replacement confirmation is required")
var ErrRecoveryPending = errors.New("an interrupted recovery requires reconciliation")

type RecoverySummary struct {
	Contacts                 int    `json:"contacts"`
	Messages                 uint64 `json:"messages"`
	Attachments              uint64 `json:"attachments"`
	SourceRequiresPassphrase bool   `json:"sourceRequiresPassphrase"`
	ReplaceRequired          bool   `json:"replaceRequired"`
	CredentialResetRequired  bool   `json:"credentialResetRequired"`
}
type portableApplication struct {
	Version            int                 `json:"version"`
	RequiresPassphrase bool                `json:"requiresPassphrase"`
	State              applicationEnvelope `json:"state"`
}
type RecoveryImport struct{ *recoveryImportState }
type recoveryImportState struct {
	mu                 sync.Mutex
	directory, stageID string
	stage              string
	master, payload    []byte
	portable           portableApplication
	summary            RecoverySummary
	verified           recovery.Verified
	closed, committed  bool
	testStep           func(string) error
}

func (RecoveryImport) String() string               { return "[private verified WhatsUp recovery]" }
func (RecoveryImport) GoString() string             { return "[private verified WhatsUp recovery]" }
func (RecoveryImport) MarshalJSON() ([]byte, error) { return nil, ErrRecovery }
func (r *RecoveryImport) Summary() RecoverySummary {
	if r == nil || r.recoveryImportState == nil {
		return RecoverySummary{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.summary
}

type contextRecoveryReader struct {
	ctx context.Context
	io.Reader
}

func (r contextRecoveryReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func hashRecoveryFile(ctx context.Context, path string) (recovery.Entry, error) {
	if err := regular(path); err != nil {
		return recovery.Entry{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return recovery.Entry{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return recovery.Entry{}, ErrUnsafePath
	}
	hash := sha256.New()
	n, err := io.CopyBuffer(hash, contextRecoveryReader{ctx, f}, make([]byte, 64<<10))
	if err != nil {
		return recovery.Entry{}, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(info, after) || info.Size() != after.Size() || info.ModTime() != after.ModTime() {
		return recovery.Entry{}, ErrRecovery
	}
	entry := recovery.Entry{Size: uint64(n)}
	copy(entry.SHA256[:], hash.Sum(nil))
	return entry, nil
}

// snapshotObjects checks database ownership, object indexes and ciphertext
// authentication before a consistent, quiesced snapshot becomes portable.
func (a *ApplicationState) snapshotObjects(ctx context.Context) ([]recovery.Entry, RecoverySummary, error) {
	var entries []recovery.Entry
	expected := map[string]uint64{}
	for _, contact := range a.state.Contacts {
		scope, err := a.messages.Scope(contact.StoreID)
		if err != nil {
			return nil, RecoverySummary{}, err
		}
		files, err := a.attachments.Scope(contact.StoreID, scope.Attachments())
		if err != nil {
			return nil, RecoverySummary{}, err
		}
		if err := files.VerifyObjects(ctx); err != nil {
			return nil, RecoverySummary{}, err
		}
		var cursor uint64
		for {
			page, next, more, err := scope.Attachments().Page(cursor, 32)
			if err != nil {
				return nil, RecoverySummary{}, err
			}
			for _, e := range page {
				if e.ObjectID == "" {
					continue
				}
				path := filepath.Join(a.vault.directory, "attachments", e.ObjectID+".bin")
				info, err := os.Lstat(path)
				if os.IsNotExist(err) && e.CipherBytes == 0 {
					continue
				}
				if err != nil || uint64(info.Size()) != e.CipherBytes {
					return nil, RecoverySummary{}, ErrRecovery
				}
				if _, exists := expected[e.ObjectID]; exists {
					return nil, RecoverySummary{}, ErrRecovery
				}
				expected[e.ObjectID] = e.CipherBytes
			}
			if !more {
				break
			}
			cursor = next
		}
	}
	info, err := a.messages.SnapshotInfo()
	if err != nil {
		return nil, RecoverySummary{}, err
	}
	scopes := make([]string, 0, len(a.state.Contacts))
	for _, c := range a.state.Contacts {
		scopes = append(scopes, c.StoreID)
	}
	sort.Strings(scopes)
	if len(scopes) != len(info.Scopes) {
		return nil, RecoverySummary{}, ErrRecovery
	}
	for i, id := range scopes {
		if id != info.Scopes[i] {
			return nil, RecoverySummary{}, ErrRecovery
		}
	}
	for id := range expected {
		entry, err := hashRecoveryFile(ctx, filepath.Join(a.vault.directory, "attachments", id+".bin"))
		if err != nil {
			return nil, RecoverySummary{}, err
		}
		entry.Kind, entry.ID = recovery.Object, id
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].ID) < strings.ToLower(entries[j].ID) })
	for i := 1; i < len(entries); i++ {
		if strings.EqualFold(entries[i-1].ID, entries[i].ID) {
			return nil, RecoverySummary{}, ErrRecovery
		}
	}
	return entries, RecoverySummary{Contacts: len(a.state.CommittedContacts), Messages: info.Messages, Attachments: info.Attachments, SourceRequiresPassphrase: a.vault.protectionMode == "os-plus-passphrase"}, nil
}

// The service stops and joins controllers before calling ExportRecovery, and
// rebuilds them afterwards because this snapshot retires their store handles.
func (a *ApplicationState) ExportRecovery(ctx context.Context, dst io.Writer, secret []byte) (result error) {
	if a == nil || a.vault == nil || ctx == nil || dst == nil {
		return ErrRecovery
	}
	if keymaterial.ValidateSecret(secret) != nil {
		return ErrPassphrase
	}
	v := a.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return err
	}
	objects, summary, err := a.snapshotObjects(ctx)
	if err != nil {
		return err
	}
	state := a.cloneLocked()
	state.MigrationBackup = ""
	state.MigrationBackupHash = ""
	portable := portableApplication{Version: 1, RequiresPassphrase: summary.SourceRequiresPassphrase, State: state}
	payload, err := json.Marshal(portable)
	if err != nil || len(payload) > MaxStateBytes {
		return ErrRecovery
	}
	defer clear(payload)
	if err := errors.Join(a.closeAttachmentStoreLocked(), a.closeMessageStoreLocked()); err != nil {
		a.failed = true
		return err
	}
	defer func() {
		if err := a.openMessageStoreLocked(); err != nil {
			a.failed = true
			result = errors.Join(result, err)
			return
		}
		if err := a.openAttachmentStoreLocked(); err != nil {
			a.failed = true
			result = errors.Join(result, err)
		}
	}()
	master := bytes.Clone(v.master)
	defer clear(master)
	db, err := hashRecoveryFile(ctx, filepath.Join(v.directory, "messages.sqlite"))
	if err != nil {
		return err
	}
	db.Kind = recovery.Database
	entries := []recovery.Entry{{Kind: recovery.Master, Size: 32, SHA256: sha256.Sum256(master)}, {Kind: recovery.Application, Size: uint64(len(payload)), SHA256: sha256.Sum256(payload)}, db}
	entries = append(entries, objects...)
	return recovery.Write(ctx, dst, secret, entries, func(ctx context.Context, e recovery.Entry) (io.ReadCloser, error) {
		switch e.Kind {
		case recovery.Master:
			return io.NopCloser(bytes.NewReader(master)), nil
		case recovery.Application:
			return io.NopCloser(bytes.NewReader(payload)), nil
		case recovery.Database:
			if regular(filepath.Join(v.directory, "messages.sqlite")) != nil {
				return nil, ErrUnsafePath
			}
			return os.Open(filepath.Join(v.directory, "messages.sqlite"))
		case recovery.Object:
			filename := filepath.Join(v.directory, "attachments", e.ID+".bin")
			if regular(filename) != nil {
				return nil, ErrUnsafePath
			}
			return os.Open(filename)
		}
		return nil, ErrRecovery
	})
}

func StageRecovery(ctx context.Context, directory string, src io.Reader, secret []byte) (result *RecoveryImport, err error) {
	if ctx == nil || src == nil || !filepath.IsAbs(directory) || keymaterial.ValidateSecret(secret) != nil {
		return nil, ErrRecovery
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if err := privateRecoveryDirectory(directory); err != nil {
		return nil, err
	}
	id, err := randomStoreID()
	if err != nil {
		return nil, err
	}
	r := &RecoveryImport{&recoveryImportState{directory: directory, stageID: id, stage: filepath.Join(directory, ".recovery-stage-"+id)}}
	if os.Mkdir(r.stage, 0700) != nil {
		return nil, ErrRecovery
	}
	defer func() {
		if err != nil {
			_ = r.Close()
		}
	}()
	if os.Mkdir(filepath.Join(r.stage, "attachments"), 0700) != nil {
		return nil, ErrRecovery
	}
	r.verified, err = recovery.Read(ctx, src, secret, func(ctx context.Context, e recovery.Entry, reader io.Reader) error {
		switch e.Kind {
		case recovery.Master:
			r.master, err = io.ReadAll(io.LimitReader(reader, 33))
			if err != nil || len(r.master) != 32 {
				return ErrRecovery
			}
			return nil
		case recovery.Application:
			r.payload, err = io.ReadAll(io.LimitReader(reader, MaxStateBytes+1))
			if err != nil || len(r.payload) > MaxStateBytes {
				return ErrRecovery
			}
			return nil
		case recovery.Database, recovery.Object:
			path := filepath.Join(r.stage, "messages.sqlite")
			if e.Kind == recovery.Object {
				path = filepath.Join(r.stage, "attachments", e.ID+".bin")
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return ErrRecovery
			}
			n, copyErr := io.CopyBuffer(file, contextRecoveryReader{ctx, reader}, make([]byte, 64<<10))
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil || syncErr != nil || closeErr != nil || uint64(n) != e.Size {
				return ErrRecovery
			}
			return nil
		}
		return ErrRecovery
	})
	if err != nil || !r.verified.Valid() {
		return nil, ErrRecovery
	}
	if decodeCanonical(r.payload, &r.portable) != nil || r.portable.Version != 1 || validateEnvelope(r.portable.State) != nil || r.portable.State.MessageStoreState != "ready" || len(r.portable.State.DeleteScopes) != 0 || r.portable.State.MigrationBackup != "" || r.portable.State.MigrationBackupHash != "" {
		return nil, ErrRecovery
	}
	if err := r.validateStage(ctx); err != nil {
		return nil, err
	}
	r.summary.SourceRequiresPassphrase = r.portable.RequiresPassphrase
	r.summary.ReplaceRequired = backendRecoveryDataExists(directory)
	return r, nil
}

func (r *RecoveryImport) validateStage(ctx context.Context) error {
	key, err := keymaterial.RandomKey()
	if err != nil {
		return err
	}
	defer clear(key)
	v := &Vault{&vaultState{directory: r.stage, key: bytes.Clone(key), master: bytes.Clone(r.master), masterPersisted: true}}
	defer v.Close()
	payload, err := json.Marshal(r.portable.State)
	if err != nil {
		return ErrRecovery
	}
	defer clear(payload)
	if err := v.saveLocked(payload); err != nil {
		return err
	}
	app, err := OpenApplication(v)
	if err != nil {
		return err
	}
	objects, summary, err := app.snapshotObjects(ctx)
	if err != nil {
		return err
	}
	files, err := os.ReadDir(filepath.Join(r.stage, "attachments"))
	if err != nil || len(files) != len(objects) {
		return ErrRecovery
	}
	r.summary = summary
	return nil
}

func (r *RecoveryImport) InspectTarget(keys KeyStore) (RecoverySummary, error) {
	if r == nil || r.recoveryImportState == nil || keys == nil {
		return RecoverySummary{}, ErrRecovery
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.committed || !r.verified.Valid() {
		return RecoverySummary{}, ErrRecovery
	}
	r.summary.ReplaceRequired = backendRecoveryDataExists(r.directory)
	_, err := keys.Get()
	if errors.Is(err, keyring.ErrNotFound) {
		r.summary.CredentialResetRequired = r.summary.ReplaceRequired
	} else if err != nil {
		return RecoverySummary{}, ErrCredential
	}
	return r.summary, nil
}

func backendRecoveryDataExists(directory string) bool {
	for _, name := range []string{"state.vault", "messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal", "app-preferences.vault"} {
		if _, err := os.Lstat(filepath.Join(directory, name)); !os.IsNotExist(err) {
			return true
		}
	}
	entries, err := os.ReadDir(filepath.Join(directory, "attachments"))
	return err != nil && !os.IsNotExist(err) || len(entries) > 0
}

func (r *RecoveryImport) Close() error {
	if r == nil || r.recoveryImportState == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	clear(r.master)
	r.master = nil
	clear(r.payload)
	r.payload = nil
	for i := range r.portable.State.Identity.Seed {
		r.portable.State.Identity.Seed[i] = 0
	}
	r.portable = portableApplication{}
	r.summary = RecoverySummary{}
	r.verified = recovery.Verified{}
	if r.committed {
		return nil
	}
	return removeRecoveryStage(r.directory, r.stageID)
}

func decodeCredential(encoded string) ([]byte, error) {
	raw, err := base64.RawStdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != 32 {
		clear(raw)
		return nil, ErrCredential
	}
	return raw, nil
}
