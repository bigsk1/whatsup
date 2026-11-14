package transfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// memoryRepository models only the durable-unit contract. It deliberately drops
// a partial unit on Resume, so a passing retry test cannot rely on buffered data.
type memoryRepository struct {
	mu           sync.Mutex
	records      map[string]Record
	data         map[string][]byte
	appendCalls  int
	finalGate    <-chan struct{}
	finalStarted chan struct{}
	capacity     bool
}

func newMemoryRepository() *memoryRepository {
	return &memoryRepository{records: map[string]Record{}, data: map[string][]byte{}}
}
func fixtureOffer(id byte, data []byte) Offer {
	var raw [16]byte
	raw[0] = id
	return Offer{ID: base64.RawURLEncoding.EncodeToString(raw[:]), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(data)), SHA256: sha256.Sum256(data)}
}
func (r *memoryRepository) stage(o Offer, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[o.ID] = Record{Offer: o, Direction: Outgoing, State: Draft}
	r.data[o.ID] = bytes.Clone(data)
}
func (r *memoryRepository) List() ([]Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.records))
	for id := range r.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Record, 0, len(ids))
	for _, id := range ids {
		out = append(out, r.records[id])
	}
	return out, nil
}
func (r *memoryRepository) Get(id string) (Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok {
		return Record{}, ErrUnavailable
	}
	return v, nil
}
func (r *memoryRepository) QueueOutgoing(id string, allow bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok || v.Direction != Outgoing || v.State != Draft {
		return ErrUnavailable
	}
	v.State = Queued
	v.AllowRelayedLarge = allow
	r.records[id] = v
	return nil
}
func (r *memoryRepository) Offer(o Offer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.records[o.ID]; ok {
		if v.Offer != o || v.Direction != Incoming {
			return ErrConflict
		}
		return nil
	}
	r.records[o.ID] = Record{Offer: o, Direction: Incoming, State: Offered}
	return nil
}
func (r *memoryRepository) Accept(id string) (Progress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok || v.State != Offered || v.Direction != Incoming {
		return Progress{}, ErrUnavailable
	}
	if r.capacity {
		return Progress{}, ErrCapacity
	}
	v.State = Accepted
	r.records[id] = v
	return Progress{v.NextChunk}, nil
}
func (r *memoryRepository) Append(id string, index uint32, data []byte) (Progress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok || v.State != Accepted || uint64(len(r.data[id])) != uint64(index)*ChunkBytes {
		return Progress{}, ErrUnavailable
	}
	if r.capacity {
		return Progress{}, ErrCapacity
	}
	r.appendCalls++
	r.data[id] = append(r.data[id], data...)
	next := index + 1
	if next%UnitChunks == 0 || next == v.Offer.Chunks() {
		v.NextChunk = next
		r.records[id] = v
	}
	return Progress{v.NextChunk}, nil
}
func (r *memoryRepository) Resume(id string) (Progress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok || v.State != Accepted && v.State != Paused && v.State != Queued {
		return Progress{}, ErrUnavailable
	}
	if v.Direction == Incoming {
		v.State = Accepted
		n := min(uint64(v.NextChunk)*ChunkBytes, v.Offer.Size)
		r.data[id] = r.data[id][:int(n)]
	} else {
		v.State = Queued
	}
	r.records[id] = v
	return Progress{v.NextChunk}, nil
}
func (r *memoryRepository) Finalize(ctx context.Context, id string) error {
	r.mu.Lock()
	v := r.records[id]
	data := bytes.Clone(r.data[id])
	gate, started := r.finalGate, r.finalStarted
	r.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if uint64(len(data)) != v.Offer.Size || sha256.Sum256(data) != v.Offer.SHA256 {
		return ErrIntegrity
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	v = r.records[id]
	if v.State != Accepted {
		return ErrUnavailable
	}
	v.State = Complete
	r.records[id] = v
	return nil
}
func (r *memoryRepository) Cancel(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok || v.State == Complete || v.State == Declined {
		return ErrUnavailable
	}
	v.State = Paused
	r.records[id] = v
	return nil
}
func (r *memoryRepository) Decline(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok {
		return ErrUnavailable
	}
	v.State = Declined
	r.records[id] = v
	return nil
}
func (r *memoryRepository) MarkDelivered(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok || v.Direction != Outgoing {
		return ErrUnavailable
	}
	v.State = Complete
	v.NextChunk = v.Offer.Chunks()
	r.records[id] = v
	return nil
}

type readSeekCloser struct{ *bytes.Reader }

func (readSeekCloser) Close() error { return nil }
func (r *memoryRepository) Open(ctx context.Context, id string) (io.ReadSeekCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok || v.Direction != Outgoing || ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	return readSeekCloser{bytes.NewReader(r.data[id])}, nil
}
func (r *memoryRepository) AllowRelayedLarge(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok {
		return ErrUnavailable
	}
	v.AllowRelayedLarge = true
	r.records[id] = v
	return nil
}

func testEngine(t *testing.T, r Repository, options Options) *Engine {
	t.Helper()
	e, err := New(r, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}
func beginTest(t *testing.T, e *Engine) uint64 {
	t.Helper()
	epoch, err := e.Begin(context.Background(), func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return epoch
}
func nextOutput(t *testing.T, e *Engine) Outbound {
	t.Helper()
	select {
	case out := <-e.Output():
		return out
	case f := <-e.Failures():
		t.Fatalf("unexpected engine failure: %v", f.Err)
	case <-time.After(3 * time.Second):
		t.Fatal("attachment output timed out")
	}
	return Outbound{}
}
func deliver(t *testing.T, from, to *Engine, targetEpoch uint64, out Outbound) {
	t.Helper()
	if err := from.Written(out.Epoch, out.Frame); err != nil {
		t.Fatal(err)
	}
	if err := to.Submit(targetEpoch, out.Frame); err != nil {
		t.Fatal(err)
	}
}
func barrier(t *testing.T, e *Engine) {
	t.Helper()
	if _, err := e.Summaries(); err != nil {
		t.Fatal(err)
	}
}
func pumpUntil(t *testing.T, a, b *Engine, ae, be uint64, done func() bool, inspect func(bool, Frame) bool) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for !done() {
		select {
		case out := <-a.Output():
			if inspect == nil || inspect(true, out.Frame) {
				deliver(t, a, b, be, out)
			} else {
				if err := a.Written(out.Epoch, out.Frame); err != nil {
					t.Fatal(err)
				}
			}
		case out := <-b.Output():
			if inspect == nil || inspect(false, out.Frame) {
				deliver(t, b, a, ae, out)
			} else {
				if err := b.Written(out.Epoch, out.Frame); err != nil {
					t.Fatal(err)
				}
			}
		case f := <-a.Failures():
			t.Fatalf("sender failed: %v", f.Err)
		case f := <-b.Failures():
			t.Fatalf("receiver failed: %v", f.Err)
		case <-deadline.C:
			t.Fatal("transfer did not reach expected state")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestExplicitApprovalDurableCompletionAndLostCompleteACK(t *testing.T) {
	data := bytes.Repeat([]byte("synthetic attachment\n"), 110000)
	o := fixtureOffer(1, data)
	ar, br := newMemoryRepository(), newMemoryRepository()
	ar.stage(o, data)
	a, b := testEngine(t, ar, Options{}), testEngine(t, br, Options{})
	ae, be := beginTest(t, a), beginTest(t, b)
	select {
	case <-a.Output():
		t.Fatal("draft went onto network")
	case <-time.After(20 * time.Millisecond):
	}
	if err := a.Queue(o.ID, false); err != nil {
		t.Fatal(err)
	}
	out := nextOutput(t, a)
	if out.Frame.Kind != OfferFrame {
		t.Fatal("first frame is not offer")
	}
	deliver(t, a, b, be, out)
	barrier(t, b)
	br.mu.Lock()
	calls := br.appendCalls
	br.mu.Unlock()
	if calls != 0 {
		t.Fatal("bytes stored before receiver approval")
	}
	select {
	case <-a.Output():
		t.Fatal("bytes sent before approval")
	case <-time.After(20 * time.Millisecond):
	}
	if err := b.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	pumpUntil(t, a, b, ae, be, func() bool { v, _ := br.Get(o.ID); return v.State == Complete }, func(sender bool, f Frame) bool { return sender || f.Kind != CompleteFrame })
	// Lose the final ACK. A fresh session reoffers, and the durable verified
	// receiver returns Complete without receiving the file a second time.
	if err := a.End(ae); err != nil {
		t.Fatal(err)
	}
	if err := b.End(be); err != nil {
		t.Fatal(err)
	}
	ae, be = beginTest(t, a), beginTest(t, b)
	chunks := 0
	pumpUntil(t, a, b, ae, be, func() bool { v, _ := ar.Get(o.ID); return v.State == Complete }, func(_ bool, f Frame) bool {
		if f.Kind == ChunkFrame {
			chunks++
		}
		return true
	})
	if chunks != 0 {
		t.Fatal("lost final ACK resent verified bytes")
	}
	br.mu.Lock()
	equal := bytes.Equal(br.data[o.ID], data)
	br.mu.Unlock()
	if !equal {
		t.Fatal("received data differs")
	}
}

func TestResumeReplaysOnlyIncompleteDurableUnit(t *testing.T) {
	data := bytes.Repeat([]byte{73}, ChunkBytes*40+19)
	o := fixtureOffer(2, data)
	ar, br := newMemoryRepository(), newMemoryRepository()
	ar.stage(o, data)
	a, b := testEngine(t, ar, Options{}), testEngine(t, br, Options{})
	ae, be := beginTest(t, a), beginTest(t, b)
	if err := a.Queue(o.ID, false); err != nil {
		t.Fatal(err)
	}
	deliver(t, a, b, be, nextOutput(t, a))
	barrier(t, b)
	if err := b.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	chunks := 0
	pumpUntil(t, a, b, ae, be, func() bool { return chunks == 19 }, func(sender bool, f Frame) bool {
		if sender && f.Kind == ChunkFrame {
			chunks++
		}
		return true
	})
	barrier(t, b)
	v, _ := br.Get(o.ID)
	if v.NextChunk != 16 {
		t.Fatalf("durable cursor=%d", v.NextChunk)
	}
	if err := a.End(ae); err != nil {
		t.Fatal(err)
	}
	if err := b.End(be); err != nil {
		t.Fatal(err)
	}
	ae, be = beginTest(t, a), beginTest(t, b)
	first := uint32(999)
	pumpUntil(t, a, b, ae, be, func() bool { v, _ := ar.Get(o.ID); return v.State == Complete }, func(sender bool, f Frame) bool {
		if sender && f.Kind == ChunkFrame && first == 999 {
			first = f.Index
		}
		return true
	})
	if first != 16 {
		t.Fatalf("resumed index=%d, want durable16", first)
	}
}

func TestCreditWindowDoesNotExceedOneUnit(t *testing.T) {
	data := bytes.Repeat([]byte{52}, ChunkBytes*40)
	o := fixtureOffer(3, data)
	r := newMemoryRepository()
	r.stage(o, data)
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	if err := e.Queue(o.ID, false); err != nil {
		t.Fatal(err)
	}
	offer := nextOutput(t, e)
	if err := e.Written(epoch, offer.Frame); err != nil {
		t.Fatal(err)
	}
	if err := e.Submit(epoch, creditControl(AcceptFrame, o, 0, [16]byte{1})); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < UnitChunks; i++ {
		out := nextOutput(t, e)
		if out.Frame.Kind != ChunkFrame || out.Frame.Index != uint32(i) {
			t.Fatal("chunk order differs")
		}
		if err := e.Written(epoch, out.Frame); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-e.Output():
		t.Fatal("sender exceeded receiver durable credit")
	case <-time.After(30 * time.Millisecond):
	}
	if err := e.Submit(epoch, creditControl(CheckpointFrame, o, 16, [16]byte{1})); err != nil {
		t.Fatal(err)
	}
	if out := nextOutput(t, e); out.Frame.Index != 16 {
		t.Fatal("checkpoint did not release next unit")
	}
}

func TestSessionRevocationBlocksRepositoryAndHiddenSummaries(t *testing.T) {
	r := newMemoryRepository()
	e := testEngine(t, r, Options{})
	var revoked atomic.Bool
	epoch, err := e.Begin(context.Background(), func() error {
		if revoked.Load() {
			return ErrUnavailable
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	o := fixtureOffer(4, []byte("private synthetic"))
	if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if _, err := r.Get(o.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("revoked session persisted an offer")
	}
	r.stage(o, []byte("private synthetic"))
	r.mu.Lock()
	v := r.records[o.ID]
	v.Hidden = true
	r.records[o.ID] = v
	r.mu.Unlock()
	s, err := e.Summaries()
	if err != nil || len(s) != 0 {
		t.Fatal("hidden attachment leaked into summaries")
	}
}

func TestLargeRelayWarningPersistsOnlyExplicitConsent(t *testing.T) {
	r := newMemoryRepository()
	o := fixtureOffer(5, []byte("synthetic"))
	o.Size = LargeFileBytes + 1
	r.stage(o, nil)
	var direct atomic.Bool
	direct.Store(true)
	e := testEngine(t, r, Options{MaySend: func(Record) bool { return direct.Load() }})
	if err := e.Queue(o.ID, false); err != nil {
		t.Fatal(err)
	}
	v, _ := r.Get(o.ID)
	if v.AllowRelayedLarge {
		t.Fatal("direct status granted permanent relay consent")
	}
	direct.Store(false)
	s, err := e.Summaries()
	if err != nil || len(s) != 1 || s[0].Warning != "relay-approval" {
		t.Fatal("path change did not request relay approval")
	}
	if err := e.Cancel(o.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Resume(o.ID); !errors.Is(err, ErrRelayApproval) {
		t.Fatal("resume bypassed relay approval")
	}
	if err := e.AllowRelay(o.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.Resume(o.ID); err != nil {
		t.Fatal(err)
	}
	v, _ = r.Get(o.ID)
	if !v.AllowRelayedLarge || v.State != Queued {
		t.Fatal("explicit consent not retained")
	}
}
