package attachmentstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/tink-crypto/tink-go/v2/insecuresecretdataaccess"
	"github.com/tink-crypto/tink-go/v2/keyset"
	"github.com/tink-crypto/tink-go/v2/secretdata"
	"github.com/tink-crypto/tink-go/v2/streamingaead"
	"github.com/tink-crypto/tink-go/v2/streamingaead/aesgcmhkdf"
	"github.com/tink-crypto/tink-go/v2/tink"
	"whatsup/internal/media"
	"whatsup/internal/transfer"
)

type Store struct{ *storeState }
type storeState struct {
	mu        sync.Mutex
	directory string
	lock      *flock.Flock
	lockInfo  os.FileInfo
	scopes    map[string]*Scope
	closed    bool
}
type Scope struct{ *scopeState }
type scopeState struct {
	mu         sync.Mutex
	store      *Store
	id, epoch  string
	meta       Metadata
	ctx        context.Context
	cancel     context.CancelFunc
	closed     bool
	operations sync.WaitGroup
	pending    map[string]*pendingUnit
	readers    map[*fileReader]bool
}
type pendingUnit struct {
	entry Entry
	data  []byte
	next  uint32
}

func (Store) String() string               { return "[private WhatsUp attachment objects]" }
func (Store) GoString() string             { return "[private WhatsUp attachment objects]" }
func (Store) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }
func (Scope) String() string               { return "[private approved WhatsUp attachments]" }
func (Scope) GoString() string             { return "[private approved WhatsUp attachments]" }
func (Scope) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

var _ transfer.Repository = (*Scope)(nil)

func private(path string, dir bool) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || dir && !info.IsDir() || !dir && !info.Mode().IsRegular() || !owned(info) || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return ErrUnsafePath
	}
	return nil
}
func Open(directory string) (*Store, error) {
	if !filepath.IsAbs(directory) || private(directory, true) != nil {
		return nil, ErrUnsafePath
	}
	objects := filepath.Join(directory, Filename)
	if os.MkdirAll(objects, 0700) != nil || private(objects, true) != nil {
		return nil, ErrUnsafePath
	}
	path := filepath.Join(directory, LockFilename)
	if _, err := os.Lstat(path); err == nil && private(path, false) != nil {
		return nil, ErrUnsafePath
	} else if err != nil && !os.IsNotExist(err) {
		return nil, ErrUnsafePath
	}
	lock := flock.New(path, flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil || !ok {
		_ = lock.Close()
		return nil, ErrUnavailable
	}
	info, err := os.Lstat(path)
	if err != nil || private(path, false) != nil {
		_ = lock.Close()
		return nil, ErrUnsafePath
	}
	return &Store{&storeState{directory: objects, lock: lock, lockInfo: info, scopes: map[string]*Scope{}}}, nil
}
func (s *Store) Scope(id string, meta Metadata) (*Scope, error) {
	if s == nil || s.storeState == nil || !transfer.ValidID(id) || meta == nil || !transfer.ValidID(meta.Epoch()) {
		return nil, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrUnavailable
	}
	if old := s.scopes[id]; old != nil {
		if old.epoch == meta.Epoch() {
			old.mu.Lock()
			closed := old.closed
			old.mu.Unlock()
			if closed {
				return nil, ErrUnavailable
			}
			return old, nil
		}
		old.retire()
	}
	ctx, cancel := context.WithCancel(context.Background())
	current := &Scope{&scopeState{store: s, id: id, epoch: meta.Epoch(), meta: meta, ctx: ctx, cancel: cancel, pending: map[string]*pendingUnit{}, readers: map[*fileReader]bool{}}}
	s.scopes[id] = current
	if err := current.recover(); err != nil {
		current.retire()
		delete(s.scopes, id)
		return nil, err
	}
	return current, nil
}
func (s *Scope) usableLocked() error {
	if s == nil || s.scopeState == nil || s.closed || s.ctx.Err() != nil {
		return ErrUnavailable
	}
	if private(s.store.directory, true) != nil {
		return ErrUnsafePath
	}
	current, err := os.Lstat(filepath.Join(filepath.Dir(s.store.directory), LockFilename))
	if err != nil || !os.SameFile(current, s.store.lockInfo) {
		return ErrUnsafePath
	}
	return nil
}
func (s *Scope) object(id string) string { return filepath.Join(s.store.directory, id+".bin") }
func randomID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	value := base64.RawURLEncoding.EncodeToString(id[:])
	if !transfer.ValidID(value) {
		return "", ErrIntegrity
	}
	return value, nil
}
func allocate(e *Entry) error {
	if e.ObjectID != "" {
		return nil
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	e.ObjectID, e.Key = id, key
	return nil
}
func primitive(key []byte) (tink.StreamingAEAD, error) {
	p, err := aesgcmhkdf.NewParameters(aesgcmhkdf.ParametersOpts{KeySizeInBytes: 32, DerivedKeySizeInBytes: 32, HKDFHashType: aesgcmhkdf.SHA256, SegmentSizeInBytes: 1 << 20})
	if err != nil {
		return nil, ErrIntegrity
	}
	k, err := aesgcmhkdf.NewKey(p, secretdata.NewBytesFromData(key, insecuresecretdataaccess.Token{}))
	if err != nil {
		return nil, ErrIntegrity
	}
	m := keyset.NewManager()
	id, err := m.AddKey(k)
	if err != nil {
		return nil, ErrIntegrity
	}
	if m.SetPrimary(id) != nil {
		return nil, ErrIntegrity
	}
	handle, err := m.Handle()
	if err != nil {
		return nil, ErrIntegrity
	}
	return streamingaead.New(handle)
}
func (s *Scope) aad(e Entry, index uint32, plain uint32) []byte {
	wire := []byte("WhatsUp attachment Tink unit v1\x00")
	for _, part := range []string{s.id, e.Record.Offer.ID, e.ObjectID, e.Record.Direction} {
		wire = binary.BigEndian.AppendUint16(wire, uint16(len(part)))
		wire = append(wire, part...)
	}
	wire = append(wire, e.Record.Offer.SHA256[:]...)
	wire = binary.BigEndian.AppendUint64(wire, e.Record.Offer.Size)
	wire = binary.BigEndian.AppendUint32(wire, index)
	return binary.BigEndian.AppendUint32(wire, plain)
}
func (s *Scope) openObject(e Entry, write bool) (*os.File, error) {
	if !transfer.ValidID(e.ObjectID) {
		return nil, ErrIntegrity
	}
	path := s.object(e.ObjectID)
	if _, err := os.Lstat(path); os.IsNotExist(err) && write && e.CipherBytes == 0 {
		f, createErr := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return nil, ErrUnsafePath
		}
		if syncDirectory(s.store.directory) != nil {
			_ = f.Close()
			return nil, ErrUnavailable
		}
		return f, nil
	}
	if private(path, false) != nil {
		return nil, ErrUnsafePath
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, ErrUnsafePath
	}
	flags := os.O_RDONLY
	if write {
		flags = os.O_RDWR
	}
	f, err := os.OpenFile(path, flags, 0)
	if err != nil {
		return nil, ErrUnavailable
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() < 0 || uint64(opened.Size()) < e.CipherBytes {
		_ = f.Close()
		return nil, ErrIntegrity
	}
	if uint64(opened.Size()) != e.CipherBytes {
		if !write || e.Ready || f.Truncate(int64(e.CipherBytes)) != nil || f.Sync() != nil {
			_ = f.Close()
			return nil, ErrIntegrity
		}
	}
	return f, nil
}
func (s *Scope) appendUnit(e Entry, plain []byte) (Entry, error) {
	if len(plain) > UnitBytes || uint64(len(plain))+e.PlainBytes() > e.Record.Offer.Size {
		return Entry{}, ErrIntegrity
	}
	f, err := s.openObject(e, true)
	if err != nil {
		return Entry{}, err
	}
	defer f.Close()
	if _, err = f.Seek(int64(e.CipherBytes), io.SeekStart); err != nil {
		return Entry{}, ErrUnavailable
	}
	crypto, err := primitive(e.Key)
	if err != nil {
		return Entry{}, err
	}
	writer, err := crypto.NewEncryptingWriter(f, s.aad(e, uint32(len(e.Units)), uint32(len(plain))))
	if err != nil {
		return Entry{}, ErrIntegrity
	}
	if written, writeErr := writer.Write(plain); writeErr != nil || written != len(plain) {
		_ = writer.Close()
		return Entry{}, ErrUnavailable
	}
	if writer.Close() != nil || f.Sync() != nil {
		return Entry{}, ErrUnavailable
	}
	end, err := f.Seek(0, io.SeekCurrent)
	if err != nil || end < int64(e.CipherBytes) || uint64(end)-e.CipherBytes > uint64(len(plain))+128 {
		return Entry{}, ErrIntegrity
	}
	unit := Unit{Offset: e.CipherBytes, CipherBytes: uint32(uint64(end) - e.CipherBytes), PlainBytes: uint32(len(plain))}
	return s.meta.Change(e.Record.Offer.ID, func(next *Entry) error {
		if next.ObjectID != e.ObjectID || next.CipherBytes != e.CipherBytes || len(next.Units) != len(e.Units) || s.ctx.Err() != nil {
			return ErrUnavailable
		}
		if next.Record.State != transfer.Accepted && next.Record.State != "draft" {
			return ErrUnavailable
		}
		next.Units = append(next.Units, unit)
		next.CipherBytes = uint64(end)
		if next.Record.Direction == transfer.Incoming {
			next.Record.NextChunk = uint32((next.PlainBytes() + transfer.ChunkBytes - 1) / transfer.ChunkBytes)
		}
		return nil
	})
}

func (s *Scope) Get(id string) (transfer.Record, error) {
	if s == nil || s.scopeState == nil {
		return transfer.Record{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return transfer.Record{}, err
	}
	e, err := s.meta.Get(id)
	r := e.Record
	r.Hidden = e.Hidden
	return r, err
}
func (s *Scope) List() ([]transfer.Record, error) {
	if s == nil || s.scopeState == nil {
		return nil, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return nil, err
	}
	entries, err := s.meta.Active()
	if err != nil {
		return nil, err
	}
	pending, err := s.meta.PendingWithdrawals()
	if err != nil {
		return nil, err
	}
	entries = append(entries, pending...)
	result := make([]transfer.Record, 0, len(entries))
	for _, e := range entries {
		if e.Record.State == transfer.Draft && !e.Ready {
			continue
		}
		r := e.Record
		r.Hidden = e.Hidden
		result = append(result, r)
	}
	return result, nil
}
func (s *Scope) History(before uint64, limit int) (records []transfer.Record, next uint64, more bool, err error) {
	if s == nil || s.scopeState == nil {
		return nil, 0, false, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.usableLocked(); err != nil {
		return
	}
	entries, next, more, err := s.meta.Page(before, limit)
	if err != nil {
		return nil, 0, false, err
	}
	for _, e := range entries {
		if !e.Hidden && (e.Record.State != "draft" || e.Ready) {
			records = append(records, e.Record)
		}
	}
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	return
}
func (s *Scope) Offer(offer transfer.Offer) error {
	if s == nil || s.scopeState == nil || offer.Validate() != nil {
		return transfer.ErrProtocol
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	if old, err := s.meta.Get(offer.ID); err == nil {
		if old.Record.Direction != transfer.Incoming || old.Record.Offer != offer {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, ErrUnavailable) {
		return err
	}
	now := time.Now().UTC()
	return s.meta.Create(Entry{Version: 1, Revision: 1, Record: transfer.Record{Offer: offer, Direction: transfer.Incoming, State: transfer.Offered, RecordedAt: now}, Units: []Unit{}, CreatedAt: now})
}
func (s *Scope) accept(id string, imageOnly bool) (transfer.Progress, error) {
	if s == nil || s.scopeState == nil {
		return transfer.Progress{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return transfer.Progress{}, err
	}
	if imageOnly {
		enabled, err := s.meta.AutoAcceptImages()
		if err != nil {
			return transfer.Progress{}, err
		}
		if !enabled {
			return transfer.Progress{}, ErrUnavailable
		}
	}
	e, err := s.meta.Change(id, func(e *Entry) error {
		if e.Record.Direction != transfer.Incoming || e.Hidden {
			return ErrUnavailable
		}
		if e.Record.State == transfer.Accepted || e.Record.State == transfer.Complete {
			return nil
		}
		if e.Record.State != transfer.Offered {
			return ErrUnavailable
		}
		if imageOnly && (e.Record.Offer.Size > MaxAutoImageBytes || !media.AllowedImage(e.Record.Offer.MediaType)) {
			return ErrUnavailable
		}
		if err := allocate(e); err != nil {
			return err
		}
		e.Approved, e.ImageOnly, e.Record.State = true, imageOnly, transfer.Accepted
		return nil
	})
	return transfer.Progress{NextChunk: e.Record.NextChunk}, err
}
func (s *Scope) Accept(id string) (transfer.Progress, error)      { return s.accept(id, false) }
func (s *Scope) AcceptImage(id string) (transfer.Progress, error) { return s.accept(id, true) }
func (s *Scope) Append(id string, index uint32, chunk []byte) (transfer.Progress, error) {
	if s == nil || s.scopeState == nil {
		return transfer.Progress{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return transfer.Progress{}, err
	}
	p := s.pending[id]
	if p == nil {
		e, err := s.meta.Get(id)
		if err != nil {
			return transfer.Progress{}, err
		}
		if e.Record.Direction != transfer.Incoming || e.Record.State != transfer.Accepted || !e.Approved {
			return transfer.Progress{}, ErrUnavailable
		}
		p = &pendingUnit{entry: e, next: e.Record.NextChunk, data: make([]byte, 0, UnitBytes)}
		s.pending[id] = p
	}
	if index != p.next {
		return transfer.Progress{NextChunk: p.entry.Record.NextChunk}, transfer.ErrProtocol
	}
	expected, err := p.entry.Record.Offer.ChunkSize(index)
	if err != nil || len(chunk) != expected {
		return transfer.Progress{}, transfer.ErrProtocol
	}
	p.data = append(p.data, chunk...)
	p.next++
	if len(p.data) == UnitBytes || p.next == p.entry.Record.Offer.Chunks() {
		next, err := s.appendUnit(p.entry, p.data)
		clear(p.data)
		delete(s.pending, id)
		if err != nil {
			return transfer.Progress{}, err
		}
		return transfer.Progress{NextChunk: next.Record.NextChunk}, nil
	}
	return transfer.Progress{NextChunk: p.entry.Record.NextChunk}, nil
}
func (s *Scope) Resume(id string) (transfer.Progress, error) {
	if s == nil || s.scopeState == nil {
		return transfer.Progress{}, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return transfer.Progress{}, err
	}
	if p := s.pending[id]; p != nil {
		clear(p.data)
		delete(s.pending, id)
	}
	e, err := s.meta.Change(id, func(e *Entry) error {
		if e.Record.State == transfer.Paused {
			if e.Record.Direction == transfer.Incoming {
				e.Record.State = transfer.Accepted
			} else if e.Ready {
				e.Record.State = transfer.Queued
			} else {
				return ErrUnavailable
			}
		} else if e.Record.State != transfer.Accepted && e.Record.State != transfer.Queued && e.Record.State != transfer.Complete {
			return ErrUnavailable
		}
		return nil
	})
	if err == nil && e.ObjectID != "" && !e.Ready {
		f, e2 := s.openObject(e, true)
		if e2 != nil {
			return transfer.Progress{}, e2
		}
		_ = f.Close()
	}
	return transfer.Progress{NextChunk: e.Record.NextChunk}, err
}
func (s *Scope) Cancel(id string) error {
	if s == nil || s.scopeState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	_, err := s.meta.Change(id, func(e *Entry) error {
		if e.Record.State != transfer.Accepted && e.Record.State != transfer.Queued && e.Record.State != transfer.Paused {
			return ErrUnavailable
		}
		e.Record.State = transfer.Paused
		return nil
	})
	if p := s.pending[id]; p != nil {
		clear(p.data)
		delete(s.pending, id)
	}
	s.cancelReadersLocked(id)
	return err
}
func (s *Scope) Decline(id string) error     { return s.remove(id, false, false) }
func (s *Scope) Withdraw(id string) error    { return s.remove(id, false, true) }
func (s *Scope) RemoveDraft(id string) error { return s.remove(id, true, false) }
func (s *Scope) remove(id string, draft, withdraw bool) error {
	if s == nil || s.scopeState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	_, err := s.meta.Change(id, func(e *Entry) error {
		if draft {
			if e.Record.Direction != transfer.Outgoing || e.Record.State != "draft" {
				return ErrUnavailable
			}
			e.Hidden = true
		} else if e.Record.State == transfer.Complete {
			if withdraw {
				return ErrUnavailable
			}
			return nil
		}
		if withdraw && e.Record.State == transfer.Declined {
			return nil
		}
		if withdraw && e.Record.State != transfer.Draft {
			e.Record.PendingWithdrawal = true
			e.Record.PeerCompleted = false
		}
		e.Record.State = transfer.Declined
		e.Approved = false
		e.ImageOnly = false
		e.Garbage = e.ObjectID != ""
		return nil
	})
	if p := s.pending[id]; p != nil {
		clear(p.data)
		delete(s.pending, id)
	}
	s.cancelReadersLocked(id)
	if err != nil {
		return err
	}
	return s.collectLocked(id)
}

func (s *Scope) AcknowledgeWithdrawal(id string, peerCompleted bool) error {
	if s == nil || s.scopeState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	_, err := s.meta.Change(id, func(e *Entry) error {
		if e.Record.State != transfer.Declined || !e.Record.PendingWithdrawal {
			return ErrUnavailable
		}
		e.Record.PendingWithdrawal = false
		e.Record.PeerCompleted = peerCompleted
		return nil
	})
	return err
}
func (s *Scope) QueueOutgoing(id string, allowRelayedLarge bool) error {
	if s == nil || s.scopeState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	_, err := s.meta.Change(id, func(e *Entry) error {
		if e.Record.Direction != transfer.Outgoing || e.Record.State != "draft" || !e.Ready || e.Hidden {
			return ErrUnavailable
		}
		e.Record.State = transfer.Queued
		e.Record.RecordedAt = time.Now().UTC()
		e.Record.AllowRelayedLarge = allowRelayedLarge
		return nil
	})
	return err
}
func (s *Scope) AllowRelayedLarge(id string) error {
	if s == nil || s.scopeState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	_, err := s.meta.Change(id, func(e *Entry) error {
		if e.Record.Direction != transfer.Outgoing || e.Hidden {
			return ErrUnavailable
		}
		e.Record.AllowRelayedLarge = true
		return nil
	})
	return err
}
func (s *Scope) MarkDelivered(id string) error {
	if s == nil || s.scopeState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	_, err := s.meta.Change(id, func(e *Entry) error {
		if e.Record.Direction == transfer.Outgoing && e.Record.State == transfer.Complete {
			return nil
		}
		if e.Record.Direction != transfer.Outgoing || !e.Ready || e.Record.State != transfer.Queued && e.Record.State != transfer.Paused && e.Record.State != transfer.Complete {
			return ErrUnavailable
		}
		e.Record.State = transfer.Complete
		e.Record.NextChunk = e.Record.Offer.Chunks()
		if e.Hidden {
			e.Garbage = true
		}
		return nil
	})
	if err == nil {
		_ = s.collectLocked(id)
	}
	return err
}
func (s *Scope) AutoAcceptImages() (bool, error) {
	if s == nil || s.scopeState == nil {
		return false, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return false, err
	}
	return s.meta.AutoAcceptImages()
}
func (s *Scope) SetAutoAcceptImages(value bool) error {
	if s == nil || s.scopeState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	return s.meta.SetAutoAcceptImages(value)
}
func (s *Scope) VerifiedMediaType(id string) (string, error) {
	if s == nil || s.scopeState == nil {
		return "", ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return "", err
	}
	e, err := s.meta.Get(id)
	if err != nil || !e.Ready || e.Hidden {
		return "", ErrUnavailable
	}
	return e.VerifiedType, nil
}

func (s *Scope) operation(ctx context.Context) (context.Context, func(), error) {
	if s == nil || s.scopeState == nil || ctx == nil {
		return nil, nil, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return nil, nil, err
	}
	s.operations.Add(1)
	work, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(ctx, cancel)
	return work, func() { stop(); cancel(); s.operations.Done() }, nil
}
func (s *Scope) Stage(ctx context.Context, offer transfer.Offer, source io.Reader) (result error) {
	if offer.Validate() != nil || source == nil {
		return transfer.ErrProtocol
	}
	work, finish, err := s.operation(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if closer, ok := source.(io.Closer); ok {
		stop := context.AfterFunc(work, func() { _ = closer.Close() })
		defer stop()
	}
	e := Entry{Version: 1, Revision: 1, Record: transfer.Record{Offer: offer, Direction: transfer.Outgoing, State: "draft"}, Units: []Unit{}, CreatedAt: time.Now().UTC()}
	if allocate(&e) != nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	err = s.meta.Create(e)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	// Only this operation's newly created draft may be cleaned up on failure.
	defer func() {
		if result != nil {
			_ = s.RemoveDraft(offer.ID)
		}
	}()
	checksum := sha256.New()
	buffer := make([]byte, UnitBytes)
	defer clear(buffer)
	remaining := offer.Size
	for remaining > 0 || offer.Size == 0 && len(e.Units) == 0 {
		if work.Err() != nil {
			return work.Err()
		}
		amount := min(uint64(UnitBytes), remaining)
		if _, err = io.ReadFull(source, buffer[:amount]); err != nil {
			return ErrIntegrity
		}
		_, _ = checksum.Write(buffer[:amount])
		s.mu.Lock()
		if s.usableLocked() != nil {
			s.mu.Unlock()
			return ErrUnavailable
		}
		e, err = s.appendUnit(e, buffer[:amount])
		s.mu.Unlock()
		if err != nil {
			return err
		}
		remaining -= amount
	}
	var excess [1]byte
	if n, err := source.Read(excess[:]); n != 0 || err != io.EOF {
		return ErrIntegrity
	}
	var actual [32]byte
	copy(actual[:], checksum.Sum(nil))
	if actual != offer.SHA256 {
		return ErrIntegrity
	}
	// An explicit save-only offer never enters an image/video decoder. Its
	// original bytes are still encrypted and authenticated by the whole-file hash.
	verified := "application/octet-stream"
	if offer.MediaType != verified {
		s.mu.Lock()
		verifiedReader, openErr := s.readerLocked(work, e, true)
		s.mu.Unlock()
		if openErr != nil {
			return openErr
		}
		var kind string
		kind, verified, err = media.Sniff(verifiedReader)
		if err == nil && kind == media.KindImage {
			if _, imageErr := media.Validate(work, verifiedReader); imageErr != nil {
				verified = "application/octet-stream"
			}
		}
		closeErr := verifiedReader.Close()
		if err != nil || closeErr != nil {
			return ErrIntegrity
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usableLocked() != nil {
		return ErrUnavailable
	}
	_, err = s.meta.Change(offer.ID, func(next *Entry) error {
		if work.Err() != nil || next.Record.State != "draft" || next.Revision != e.Revision {
			return ErrUnavailable
		}
		next.Ready = true
		next.VerifiedType = verified
		return nil
	})
	return err
}

func (s *Scope) Finalize(ctx context.Context, id string) error {
	work, finish, err := s.operation(ctx)
	if err != nil {
		return err
	}
	defer finish()
	s.mu.Lock()
	e, err := s.meta.Get(id)
	if err != nil || e.Record.Direction != transfer.Incoming || !e.Approved || e.Record.State != transfer.Accepted || e.Record.NextChunk != e.Record.Offer.Chunks() {
		s.mu.Unlock()
		return ErrUnavailable
	}
	if e.Record.Offer.Size == 0 && len(e.Units) == 0 {
		e, err = s.appendUnit(e, nil)
		if err != nil {
			s.mu.Unlock()
			return err
		}
	}
	reader, err := s.readerLocked(work, e, true)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	defer reader.Close()
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, reader, make([]byte, transfer.ChunkBytes)); err != nil {
		return err
	}
	var actual [32]byte
	copy(actual[:], hash.Sum(nil))
	if actual != e.Record.Offer.SHA256 {
		_ = s.Decline(id)
		return ErrIntegrity
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return err
	}
	kind, verified := media.KindFile, "application/octet-stream"
	if e.Record.Offer.MediaType != verified {
		kind, verified, err = media.Sniff(reader)
		if err != nil {
			return ErrIntegrity
		}
		if kind == media.KindImage {
			if _, err = media.Validate(work, reader); err != nil {
				if e.ImageOnly {
					_ = s.Decline(id)
					return ErrIntegrity
				}
				verified = "application/octet-stream"
			}
		}
	}
	if e.ImageOnly && (kind != media.KindImage || !media.AllowedImage(verified)) {
		_ = s.Decline(id)
		return ErrIntegrity
	}
	if err := reader.Close(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usableLocked() != nil || work.Err() != nil {
		return ErrUnavailable
	}
	_, err = s.meta.Change(id, func(next *Entry) error {
		if next.Record.State != transfer.Accepted || !next.Approved || next.CipherBytes != e.CipherBytes {
			return ErrUnavailable
		}
		next.Ready = true
		next.Record.State = transfer.Complete
		next.VerifiedType = verified
		if next.Hidden {
			next.Garbage = true
		}
		return nil
	})
	if err == nil {
		_ = s.collectLocked(id)
	}
	return err
}

func (s *Scope) collectLocked(id string) error {
	e, err := s.meta.Get(id)
	if err != nil {
		return err
	}
	if !e.Garbage {
		return nil
	}
	s.cancelReadersLocked(id)
	path := s.object(e.ObjectID)
	if _, err := os.Lstat(path); err == nil {
		if private(path, false) != nil {
			return ErrUnsafePath
		}
		if os.Remove(path) != nil {
			return ErrUnavailable
		}
		if syncDirectory(s.store.directory) != nil {
			return ErrUnavailable
		}
	} else if !os.IsNotExist(err) {
		return ErrUnavailable
	}
	_, err = s.meta.Change(id, func(next *Entry) error {
		if !next.Garbage || next.ObjectID != e.ObjectID {
			return ErrUnavailable
		}
		next.ObjectID = ""
		clear(next.Key)
		next.Key = nil
		next.Units = []Unit{}
		next.CipherBytes = 0
		next.Ready = false
		next.Garbage = false
		if next.Record.State != transfer.Complete {
			next.Record.NextChunk = 0
		}
		return nil
	})
	return err
}
func (s *Scope) cancelReadersLocked(id string) {
	for reader := range s.readers {
		if reader.entry.Record.Offer.ID == id {
			reader.cancel()
		}
	}
}
func (s *Scope) retire() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	for _, p := range s.pending {
		clear(p.data)
		clear(p.entry.Key)
	}
	s.pending = map[string]*pendingUnit{}
	s.mu.Unlock()
}
func (s *Scope) Close() error {
	if s == nil || s.scopeState == nil {
		return nil
	}
	s.retire()
	s.operations.Wait()
	return nil
}
func (s *Store) RetireScope(id string) {
	if s == nil || s.storeState == nil {
		return
	}
	s.mu.Lock()
	scope := s.scopes[id]
	s.mu.Unlock()
	if scope != nil {
		_ = scope.Close()
	}
}
func (s *Store) Close() error {
	if s == nil || s.storeState == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	var scopes []*Scope
	for _, scope := range s.scopes {
		scopes = append(scopes, scope)
	}
	s.mu.Unlock()
	for _, scope := range scopes {
		_ = scope.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		if s.lock.Close() != nil {
			return ErrUnavailable
		}
		s.lock = nil
	}
	return nil
}
