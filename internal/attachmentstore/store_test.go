package attachmentstore_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/messagestore"
	"whatsup/internal/transfer"
)

func id(n byte) string { return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{n}, 16)) }

type fixture struct {
	dir   string
	db    *messagestore.DB
	store *attachmentstore.Store
	scope *attachmentstore.Scope
	raw   *messagestore.Scope
	fail  bool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{dir: filepath.Join(t.TempDir(), "private")}
	f.open(t)
	t.Cleanup(func() { f.close(t) })
	return f
}
func (f *fixture) open(t *testing.T) {
	t.Helper()
	var err error
	f.db, err = messagestore.Open(f.dir, bytes.Repeat([]byte{71}, 32), messagestore.Options{BeforeCommit: func() error {
		if f.fail {
			return errors.New("synthetic transaction interruption")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	f.raw, err = f.db.Scope(id(1))
	if err != nil {
		t.Fatal(err)
	}
	f.store, err = attachmentstore.Open(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	f.scope, err = f.store.Scope(id(1), f.raw.Attachments())
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) close(t *testing.T) {
	t.Helper()
	if f.store != nil {
		if err := f.store.Close(); err != nil {
			t.Fatal(err)
		}
		f.store = nil
	}
	if f.db != nil {
		if err := f.db.Close(); err != nil {
			t.Fatal(err)
		}
		f.db = nil
	}
}
func offer(n byte, body []byte) transfer.Offer {
	return transfer.Offer{ID: id(n), Name: "synthetic-private-attachment-name.pdf", MediaType: "application/pdf", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
}
func appendBody(t *testing.T, s *attachmentstore.Scope, o transfer.Offer, body []byte, start uint32) {
	t.Helper()
	for index := start; index < o.Chunks(); index++ {
		end := min(uint64(len(body)), uint64(index+1)*transfer.ChunkBytes)
		p, err := s.Append(o.ID, index, body[uint64(index)*transfer.ChunkBytes:end])
		if err != nil {
			t.Fatalf("append %d: %v", index, err)
		}
		expected := ((index + 1) / 16) * 16
		if index+1 == o.Chunks() {
			expected = o.Chunks()
		}
		if p.NextChunk != expected {
			t.Fatalf("checkpoint %d: %d want %d", index, p.NextChunk, expected)
		}
	}
}
func readAll(t *testing.T, s *attachmentstore.Scope, o transfer.Offer) []byte {
	t.Helper()
	r, err := s.Open(context.Background(), o.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDraftExplicitSendRangeAndEncryptedFiles(t *testing.T) {
	f := newFixture(t)
	body := bytes.Repeat([]byte("synthetic-secret-attachment-content-"), 40000)
	o := offer(2, body)
	if err := f.scope.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	r, err := f.scope.Get(o.ID)
	if err != nil || r.State != transfer.Draft {
		t.Fatalf("draft: %v %v", r.State, err)
	}
	if !bytes.Equal(readAll(t, f.scope, o), body) {
		t.Fatal("draft body changed")
	}
	rangeReader, err := f.scope.Open(context.Background(), o.ID)
	if err != nil {
		t.Fatal(err)
	}
	position := int64(attachmentstore.UnitBytes - 13)
	if _, err := rangeReader.Seek(position, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	part := make([]byte, 90)
	if _, err := io.ReadFull(rangeReader, part); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(part, body[position:position+90]) {
		t.Fatal("range differs across encryption units")
	}
	rangeReader.Close()
	if err := f.scope.QueueOutgoing(o.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.scope.QueueOutgoing(o.ID, false); !errors.Is(err, transfer.ErrUnavailable) {
		t.Fatal("second explicit send accepted")
	}
	page, _, _, err := f.scope.History(0, 32)
	if err != nil || len(page) != 1 || page[0].State != transfer.Queued || !page[0].AllowRelayedLarge {
		t.Fatal("history/relay approval changed")
	}
	if err := filepath.WalkDir(f.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".lock") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, body[:100]) || bytes.Contains(data, []byte(o.Name)) {
			t.Fatal("plaintext metadata/content reached a local file")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.close(t)
	f.open(t)
	if !bytes.Equal(readAll(t, f.scope, o), body) {
		t.Fatal("reopen differs")
	}
}

func TestReceiveResumeWholeHashAndTerminalReplay(t *testing.T) {
	f := newFixture(t)
	body := bytes.Repeat([]byte("synthetic-received-content-"), 110000)
	o := offer(3, body)
	if err := f.scope.Offer(o); err != nil {
		t.Fatal(err)
	}
	if _, err := f.scope.Open(context.Background(), o.ID); err == nil {
		t.Fatal("offer was readable")
	}
	if _, err := f.scope.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 20; i++ {
		_, err := f.scope.Append(o.ID, i, body[uint64(i)*transfer.ChunkBytes:uint64(i+1)*transfer.ChunkBytes])
		if err != nil {
			t.Fatal(err)
		}
	}
	f.close(t)
	f.open(t)
	p, err := f.scope.Resume(o.ID)
	if err != nil || p.NextChunk != 16 {
		t.Fatalf("resume: %v %v", p, err)
	}
	appendBody(t, f.scope, o, body, 16)
	if _, err := f.scope.Open(context.Background(), o.ID); err == nil {
		t.Fatal("unverified body was readable")
	}
	if err := f.scope.Finalize(context.Background(), o.ID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(t, f.scope, o), body) {
		t.Fatal("received bytes differ")
	}
	if err := f.scope.Offer(o); err != nil {
		t.Fatal("lost final ACK could not reoffer")
	}
	changed := o
	changed.SHA256[0] ^= 1
	if !errors.Is(f.scope.Offer(changed), transfer.ErrConflict) {
		t.Fatal("changed offer reused ID")
	}
	f.close(t)
	f.open(t)
	r, err := f.scope.Get(o.ID)
	if err != nil || r.State != transfer.Complete {
		t.Fatal("complete evidence lost")
	}
}

func TestTamperTruncateAndSwappedEncryptedObjects(t *testing.T) {
	for _, mode := range []string{"tamper", "truncate", "swap"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			a, b := bytes.Repeat([]byte{31}, 90000), bytes.Repeat([]byte{32}, 90000)
			oa, ob := offer(4, a), offer(5, b)
			if f.scope.Stage(context.Background(), oa, bytes.NewReader(a)) != nil || f.scope.Stage(context.Background(), ob, bytes.NewReader(b)) != nil {
				t.Fatal("stage")
			}
			ma, err := f.raw.Attachments().Get(oa.ID)
			if err != nil {
				t.Fatal(err)
			}
			mb, err := f.raw.Attachments().Get(ob.ID)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.dir, "attachments", ma.ObjectID+".bin")
			encrypted, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "tamper":
				encrypted[len(encrypted)/2] ^= 1
			case "truncate":
				encrypted = encrypted[:len(encrypted)-1]
			case "swap":
				encrypted, err = os.ReadFile(filepath.Join(f.dir, "attachments", mb.ObjectID+".bin"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, encrypted, 0600); err != nil {
				t.Fatal(err)
			}
			r, err := f.scope.Open(context.Background(), oa.ID)
			if err == nil {
				_, err = io.Copy(io.Discard, r)
				r.Close()
			}
			if err == nil {
				t.Fatal("mutated encrypted object accepted")
			}
		})
	}
}

func TestInterruptedUnitCommitRepairsEncryptedTail(t *testing.T) {
	f := newFixture(t)
	body := bytes.Repeat([]byte{39}, attachmentstore.UnitBytes+123)
	o := offer(6, body)
	if f.scope.Offer(o) != nil {
		t.Fatal("offer")
	}
	if _, err := f.scope.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	for i := uint32(0); i < 15; i++ {
		if _, err := f.scope.Append(o.ID, i, body[uint64(i)*transfer.ChunkBytes:uint64(i+1)*transfer.ChunkBytes]); err != nil {
			t.Fatal(err)
		}
	}
	f.fail = true
	_, err := f.scope.Append(o.ID, 15, body[15*transfer.ChunkBytes:16*transfer.ChunkBytes])
	f.fail = false
	if err == nil {
		t.Fatal("injected commit succeeded")
	}
	f.close(t)
	f.open(t)
	p, err := f.scope.Resume(o.ID)
	if err != nil || p.NextChunk != 0 {
		t.Fatalf("uncommitted checkpoint survived: %v %v", p, err)
	}
	appendBody(t, f.scope, o, body, 0)
	if err := f.scope.Finalize(context.Background(), o.ID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(t, f.scope, o), body) {
		t.Fatal("tail repair differs")
	}
}

func TestQuotaClearHistoryAndImageOnlyApproval(t *testing.T) {
	f := newFixture(t)
	large := offer(7, []byte("dummy"))
	large.Size = transfer.MaxFileBytes
	if f.scope.Offer(large) != nil {
		t.Fatal("offer")
	}
	if _, err := f.scope.Accept(large.ID); err != nil {
		t.Fatal(err)
	}
	second := large
	second.ID = id(8)
	if f.scope.Offer(second) != nil {
		t.Fatal("second offer")
	}
	if _, err := f.scope.Accept(second.ID); !errors.Is(err, transfer.ErrCapacity) {
		t.Fatalf("quota accepted second 2GiB object: %v", err)
	}
	if err := f.scope.Decline(large.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.scope.Accept(second.ID); err != nil {
		t.Fatal("decline did not free quota", err)
	}
	if f.scope.Decline(second.ID) != nil {
		t.Fatal("decline")
	}
	body := []byte("%PDF synthetic-not-an-image")
	o := offer(9, body)
	o.MediaType = "image/png"
	if err := f.scope.SetAutoAcceptImages(true); err != nil {
		t.Fatal(err)
	}
	if f.scope.Offer(o) != nil {
		t.Fatal("image offer")
	}
	if _, err := f.scope.AcceptImage(o.ID); err != nil {
		t.Fatal(err)
	}
	appendBody(t, f.scope, o, body, 0)
	if !errors.Is(f.scope.Finalize(context.Background(), o.ID), transfer.ErrIntegrity) {
		t.Fatal("MIME claim bypassed image-only approval")
	}
	if _, err := f.scope.Open(context.Background(), o.ID); err == nil {
		t.Fatal("non-image autoaccepted content readable")
	}
	body = bytes.Repeat([]byte{67}, 2000)
	o = offer(10, body)
	if err := f.scope.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if f.scope.QueueOutgoing(o.ID, false) != nil {
		t.Fatal("queue")
	}
	if err := f.raw.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	if err := f.scope.CollectGarbage(); err != nil {
		t.Fatal(err)
	}
	r, err := f.scope.Get(o.ID)
	if err != nil || !r.Hidden || r.State != transfer.Queued {
		t.Fatal("clear lost pending send")
	}
	if err := f.scope.MarkDelivered(o.ID); err != nil {
		t.Fatal(err)
	}
	entry, err := f.raw.Attachments().Get(o.ID)
	if err != nil || entry.ObjectID != "" || entry.Record.State != transfer.Complete {
		t.Fatal("hidden delivered bytes not reclaimed")
	}
	page, _, _, err := f.scope.History(0, 32)
	if err != nil || len(page) != 0 {
		t.Fatal("clear history still visible")
	}
}

func TestZeroLengthAndFailedDraft(t *testing.T) {
	f := newFixture(t)
	o := offer(11, nil)
	if err := f.scope.Stage(context.Background(), o, bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	if len(readAll(t, f.scope, o)) != 0 {
		t.Fatal("empty changed")
	}
	bad := offer(12, []byte("expected"))
	if err := f.scope.Stage(context.Background(), bad, bytes.NewReader([]byte("changed!"))); err == nil {
		t.Fatal("source mutation accepted")
	}
	entry, err := f.raw.Attachments().Get(bad.ID)
	if err != nil || entry.Record.State != transfer.Declined || entry.ObjectID != "" {
		t.Fatal("failed draft retained bytes")
	}
	if err := f.scope.Stage(context.Background(), o, bytes.NewReader(nil)); !errors.Is(err, transfer.ErrConflict) {
		t.Fatal("duplicate draft accepted")
	}
	if _, err := f.scope.Open(context.Background(), o.ID); err != nil {
		t.Fatal("collision deleted existing draft")
	}
	g := newFixture(t)
	if err := g.scope.Offer(o); err != nil {
		t.Fatal(err)
	}
	if _, err := g.scope.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	if err := g.scope.Finalize(context.Background(), o.ID); err != nil {
		t.Fatal(err)
	}
	if len(readAll(t, g.scope, o)) != 0 {
		t.Fatal("received empty file differs")
	}
}

type generatedReader struct {
	remaining uint64
	position  uint64
}

func (r *generatedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if uint64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = byte((r.position+uint64(i))*29 + 83)
	}
	r.position += uint64(n)
	r.remaining -= uint64(n)
	return n, nil
}

// Explicit opt-in keeps routine regressions small. The source is generated in
// memory; both staged and received copies on disk contain Tink ciphertext only.
func TestOneGiBEncryptedStagingAndResumableReceive(t *testing.T) {
	if os.Getenv("WHATSUP_LARGE_ATTACHMENT_TEST") != "1" {
		t.Skip("large local IO probe is explicitly enabled")
	}
	const size = uint64(1 << 30)
	hash := sha256.New()
	if _, err := io.CopyBuffer(hash, &generatedReader{remaining: size}, make([]byte, transfer.ChunkBytes)); err != nil {
		t.Fatal(err)
	}
	o := offer(31, nil)
	o.Size = size
	copy(o.SHA256[:], hash.Sum(nil))
	sender, receiver := newFixture(t), newFixture(t)
	start := time.Now()
	if err := sender.scope.Stage(context.Background(), o, &generatedReader{remaining: size}); err != nil {
		t.Fatal(err)
	}
	if err := sender.scope.QueueOutgoing(o.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := receiver.scope.Offer(o); err != nil {
		t.Fatal(err)
	}
	if _, err := receiver.scope.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	reader, err := sender.scope.OpenOutgoing(context.Background(), o.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	chunk := make([]byte, transfer.ChunkBytes)
	for index := uint32(0); index < o.Chunks(); index++ {
		if _, err := io.ReadFull(reader, chunk); err != nil {
			t.Fatal(err)
		}
		if _, err := receiver.scope.Append(o.ID, index, chunk); err != nil {
			t.Fatal(err)
		}
		if index == o.Chunks()/2-1 {
			if err := receiver.scope.Cancel(o.ID); err != nil {
				t.Fatal(err)
			}
			receiver.close(t)
			receiver.open(t)
			progress, err := receiver.scope.Resume(o.ID)
			if err != nil || progress.NextChunk != index+1 {
				t.Fatal("large transfer lost durable checkpoint")
			}
		}
	}
	if err := receiver.scope.Finalize(context.Background(), o.ID); err != nil {
		t.Fatal(err)
	}
	if err := sender.scope.MarkDelivered(o.ID); err != nil {
		t.Fatal(err)
	}
	t.Logf("verified_bytes=%d elapsed_seconds=%.3f", size, time.Since(start).Seconds())
}
