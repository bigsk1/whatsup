package attachmentstore

import (
	"context"
	"io"
	"os"
	"sync"
	"whatsup/internal/transfer"
)

type fileReader struct {
	mu           sync.Mutex
	scope        *Scope
	entry        Entry
	file         *os.File
	info         os.FileInfo
	ctx          context.Context
	cancel       context.CancelFunc
	stop         func() bool
	stopExternal func() bool
	closeOnce    sync.Once
	closed       bool
	position     int64
	unitIndex    int
	unit         []byte
}

func (*fileReader) String() string               { return "[private decrypted WhatsUp attachment stream]" }
func (*fileReader) GoString() string             { return "[private decrypted WhatsUp attachment stream]" }
func (*fileReader) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func (s *Scope) Open(ctx context.Context, id string) (io.ReadSeekCloser, error) {
	if s == nil || s.scopeState == nil || ctx == nil {
		return nil, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return nil, err
	}
	entry, err := s.meta.Get(id)
	if err != nil {
		return nil, err
	}
	return s.readerLocked(ctx, entry, false)
}

// OpenOutgoing is retained only by the authenticated transfer runtime. Terminal
// withdrawal or Clear history revokes these reads as well as preview/save reads.
func (s *Scope) OpenOutgoing(ctx context.Context, id string) (io.ReadSeekCloser, error) {
	if s == nil || s.scopeState == nil || ctx == nil {
		return nil, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return nil, err
	}
	e, err := s.meta.Get(id)
	if err != nil {
		return nil, err
	}
	if e.Record.Direction != transfer.Outgoing || !e.Ready || e.Record.State != transfer.Queued && e.Record.State != transfer.Paused {
		return nil, ErrUnavailable
	}
	return s.readerLocked(ctx, e, true)
}
func (s *Scope) readerLocked(ctx context.Context, entry Entry, finalizing bool) (*fileReader, error) {
	if s.usableLocked() != nil || ctx.Err() != nil || entry.ObjectID == "" || !finalizing && (entry.Hidden || !entry.Ready) || finalizing && (entry.PlainBytes() != entry.Record.Offer.Size || entry.Record.Direction == "incoming" && !entry.Approved) {
		return nil, ErrUnavailable
	}
	if !finalizing && entry.Record.Direction == "incoming" && (entry.Record.State != "complete" || !entry.Approved) {
		return nil, ErrUnavailable
	}
	f, err := s.openObject(entry, false)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, ErrIntegrity
	}
	work, cancel := context.WithCancel(s.ctx)
	r := &fileReader{scope: s, entry: Clone(entry), file: f, info: info, ctx: work, cancel: cancel, unitIndex: -1}
	s.operations.Add(1)
	s.readers[r] = true
	// Arm only after both cancellation callbacks are assigned. An already
	// cancelled caller must not race initialization or retain this reader.
	armed := make(chan struct{})
	r.stopExternal = context.AfterFunc(ctx, cancel)
	r.stop = context.AfterFunc(work, func() { <-armed; _ = r.Close() })
	close(armed)
	return r, nil
}
func (r *fileReader) load(index int) error {
	if r.closed || r.ctx.Err() != nil {
		return ErrUnavailable
	}
	if index == r.unitIndex {
		return nil
	}
	if index < 0 || index >= len(r.entry.Units) {
		return ErrIntegrity
	}
	current, err := os.Lstat(r.scope.object(r.entry.ObjectID))
	if err != nil || !os.SameFile(current, r.info) || uint64(current.Size()) != r.entry.CipherBytes {
		return ErrIntegrity
	}
	unit := r.entry.Units[index]
	primitive, err := primitive(r.entry.Key)
	if err != nil {
		return err
	}
	reader, err := primitive.NewDecryptingReader(io.NewSectionReader(r.file, int64(unit.Offset), int64(unit.CipherBytes)), r.scope.aad(r.entry, uint32(index), unit.PlainBytes))
	if err != nil {
		return ErrIntegrity
	}
	plain, err := io.ReadAll(io.LimitReader(reader, UnitBytes+1))
	if err != nil || len(plain) != int(unit.PlainBytes) {
		clear(plain)
		return ErrIntegrity
	}
	clear(r.unit)
	r.unit = plain
	r.unitIndex = index
	return nil
}
func (r *fileReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return 0, ErrUnavailable
	}
	if len(p) == 0 {
		return 0, nil
	}
	if uint64(r.position) >= r.entry.Record.Offer.Size {
		if r.entry.Record.Offer.Size == 0 && len(r.entry.Units) == 1 {
			if err := r.load(0); err != nil {
				return 0, err
			}
		}
		return 0, io.EOF
	}
	index := int(r.position / UnitBytes)
	if err := r.load(index); err != nil {
		return 0, err
	}
	offset := int(r.position % UnitBytes)
	if offset >= len(r.unit) {
		return 0, ErrIntegrity
	}
	n := copy(p, r.unit[offset:])
	r.position += int64(n)
	return n, nil
}
func (r *fileReader) Seek(offset int64, whence int) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.ctx.Err() != nil {
		return 0, ErrUnavailable
	}
	var position int64
	switch whence {
	case io.SeekStart:
		position = offset
	case io.SeekCurrent:
		position = r.position + offset
	case io.SeekEnd:
		position = int64(r.entry.Record.Offer.Size) + offset
	default:
		return 0, ErrUnavailable
	}
	if position < 0 || position > int64(r.entry.Record.Offer.Size) {
		return 0, ErrUnavailable
	}
	r.position = position
	return position, nil
}
func (r *fileReader) Close() error {
	if r == nil {
		return nil
	}
	var result error
	r.closeOnce.Do(func() {
		if r.stop != nil {
			r.stop()
		}
		if r.stopExternal != nil {
			r.stopExternal()
		}
		r.cancel()
		r.mu.Lock()
		r.closed = true
		clear(r.unit)
		r.unit = nil
		clear(r.entry.Key)
		result = r.file.Close()
		r.mu.Unlock()
		r.scope.mu.Lock()
		delete(r.scope.readers, r)
		r.scope.mu.Unlock()
		r.scope.operations.Done()
	})
	return result
}
