package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"testing"
	"time"

	"whatsup/internal/transfer"
)

type gatedSourceRepository struct {
	transfer.Repository
	started chan struct{}
	release <-chan struct{}
}

func (r *gatedSourceRepository) Open(ctx context.Context, id string) (io.ReadSeekCloser, error) {
	return r.open(ctx, id, false)
}
func (r *gatedSourceRepository) OpenOutgoing(ctx context.Context, id string) (io.ReadSeekCloser, error) {
	return r.open(ctx, id, true)
}
func (r *gatedSourceRepository) open(ctx context.Context, id string, outgoing bool) (io.ReadSeekCloser, error) {
	select {
	case r.started <- struct{}{}:
	default:
	}
	select {
	case <-r.release:
		if outgoing {
			return r.Repository.(transfer.OutgoingRepository).OpenOutgoing(ctx, id)
		}
		return r.Repository.Open(ctx, id)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func connectRegistryPair(t *testing.T, a, b *registryPeer, id string) {
	t.Helper()
	if _, err := a.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, a, id, func(s Snapshot) bool { return s.State == "connected" })
	waitRegistry(t, b, id, func(s Snapshot) bool { return s.State == "connected" })
}

// This checks real Tink-encrypted objects, SQLite metadata, exact-key TLS and
// the shared packet scheduler. net.Pipe deliberately supplies no DERP/direct
// path evidence and does not stand in for native two-machine attachment QA.
func TestAttachmentEncryptedStoreTLSAndChatContinueWhileFileReadWaits(t *testing.T) {
	for _, side := range []string{"none", "sender", "receiver", "both"} {
		t.Run(side, func(t *testing.T) { attachmentStoreTLS(t, side) })
	}
}
func attachmentStoreTLS(t *testing.T, clearSide string) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	connectRegistryPair(t, a, b, id)
	// Reopen approved state with a gated source to prove file IO cannot hold
	// the TLS writer or delay delivery of a newly queued text message.
	a.registry.Close()
	b.registry.Close()
	network = &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	source, err := a.app.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("synthetic encrypted transfer\n"), 85000)
	var raw [16]byte
	raw[0] = 93
	offer := transfer.Offer{ID: base64.RawURLEncoding.EncodeToString(raw[:]), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(data)), SHA256: sha256.Sum256(data)}
	if err := source.Stage(context.Background(), offer, bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	gated := &gatedSourceRepository{Repository: source, started: make(chan struct{}, 1), release: release}
	engine, err := transfer.New(gated, transfer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := a.registry.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.transfers = engine
	manager.mu.Unlock()
	connectRegistryPair(t, a, b, id)
	if err := a.registry.WakeTransfers(id); err != nil {
		t.Fatal(err)
	}
	// A draft remains local, despite an already-authenticated live session.
	time.Sleep(20 * time.Millisecond)
	received, err := b.app.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := received.Get(offer.ID); err == nil {
		t.Fatal("draft was offered before explicit Send")
	}
	if err := a.registry.QueueTransfer(id, offer.ID, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		record, err := received.Get(offer.ID)
		if err == nil && record.State == transfer.Offered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("incoming offer not persisted")
		}
		time.Sleep(time.Millisecond)
	}
	if err := b.registry.AcceptTransfer(id, offer.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gated.started:
	case <-time.After(time.Second):
		t.Fatal("outgoing file read did not start")
	}
	if _, err := a.registry.Send(id, "synthetic text while attachment read waits"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, b, id, func(s Snapshot) bool {
		return len(s.Messages) == 1 && s.Messages[0].Text == "synthetic text while attachment read waits"
	})
	waitRegistry(t, a, id, func(s Snapshot) bool { return len(s.Messages) == 1 && s.Messages[0].Delivered })
	if clearSide == "sender" || clearSide == "both" {
		done := make(chan error, 1)
		go func() { _, err := a.registry.ClearHistory(id); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("clear did not interrupt the gated outgoing source")
		}
		hidden, err := source.Get(offer.ID)
		if err != nil || !hidden.Hidden || hidden.State != transfer.Declined {
			t.Fatal("history clear retained queued transfer", err)
		}
	}
	if clearSide == "receiver" || clearSide == "both" {
		if _, err := b.registry.ClearHistory(id); err != nil {
			t.Fatal(err)
		}
		hidden, err := received.Get(offer.ID)
		if err != nil || !hidden.Hidden || hidden.State != transfer.Declined {
			t.Fatal("receiver clear retained accepted transfer", err)
		}
	}
	close(release)
	wantState := transfer.Complete
	if clearSide != "none" {
		wantState = transfer.Declined
	}
	deadline = time.Now().Add(8 * time.Second)
	for {
		record, err := source.Get(offer.ID)
		other, otherErr := received.Get(offer.ID)
		if err == nil && otherErr == nil && record.State == wantState && other.State == wantState {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("transfer did not become %s on both sides: %s/%s", wantState, record.State, other.State)
		}
		time.Sleep(2 * time.Millisecond)
	}
	reader, err := received.Open(context.Background(), offer.ID)
	if clearSide != "none" {
		if err == nil {
			_ = reader.Close()
			t.Fatal("hidden received file became user readable")
		}
		if err := received.Offer(offer); err != nil {
			t.Fatal(err)
		}
		retained, err := received.Get(offer.ID)
		if err != nil || retained.State != transfer.Declined {
			t.Fatal("duplicate offer revived cancelled transfer")
		}
	} else if err != nil {
		t.Fatal(err)
	} else {
		actual, err := io.ReadAll(reader)
		_ = reader.Close()
		if err != nil || !bytes.Equal(data, actual) {
			t.Fatal("verified received attachment bytes differ")
		}
	}
	if _, err := a.registry.Send(id, "synthetic text after attachment completion"); err != nil {
		t.Fatal(err)
	}
	expected := 2
	if clearSide == "receiver" || clearSide == "both" {
		expected = 1
	}
	waitRegistry(t, b, id, func(s Snapshot) bool { return len(s.Messages) == expected })
	if err := a.registry.Retire(id); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.AcceptTransfer(id, offer.ID); err == nil {
		t.Fatal("retired contact retained transfer authority")
	}
}

func TestAttachmentPacketBoundsAndChatNeighbors(t *testing.T) {
	var raw [16]byte
	raw[0] = 94
	o := transfer.Offer{ID: base64.RawURLEncoding.EncodeToString(raw[:]), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: 3, SHA256: sha256.Sum256([]byte{1, 2, 3})}
	f := transfer.Frame{Kind: transfer.OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}
	var wire bytes.Buffer
	wire.WriteByte(transferPacket)
	if err := transfer.WriteFrame(&wire, f); err != nil {
		t.Fatal(err)
	}
	wire.WriteByte(pingPacket)
	wire.Write(make([]byte, 16))
	got, err := readPacket(&wire)
	if err != nil || got.kind != transferPacket || got.attachment.Offer != o {
		t.Fatal("attachment packet changed")
	}
	got, err = readPacket(&wire)
	if err != nil || got.kind != pingPacket || wire.Len() != 0 {
		t.Fatal("attachment swallowed neighboring control packet")
	}
}
