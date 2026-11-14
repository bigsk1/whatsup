package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"
	"time"

	"whatsup/internal/storage"
	"whatsup/internal/transfer"
)

func reopenWithdrawPeer(t *testing.T, peer *registryPeer) {
	t.Helper()
	peer.registry.Close()
	if err := peer.app.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err := storage.Open(peer.directory, peer.keys)
	if err != nil {
		t.Fatal(err)
	}
	peer.app, err = storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	peer.local, err = peer.app.Identity()
	if err != nil {
		t.Fatal(err)
	}
}

// Actual SQLite/Tink objects survive process-equivalent reopening. The only
// substituted component is the physical net.Pipe transport, not authentication,
// transfer negotiation, cancellation persistence, or ACK handling.
func TestPausedAttachmentCancellationOfflineAndAfterReconnectRetiresBothSides(t *testing.T) {
	for _, actor := range []string{"sender", "receiver"} {
		for _, mode := range []string{"offline-cancel", "reconnected-cancel", "offline-clear"} {
			t.Run(actor+"/"+mode, func(t *testing.T) {
				a, b := newRegistryPeer(t), newRegistryPeer(t)
				id := seedRegistryAssociation(t, a, b)
				network := &registryNetwork{}
				openRegistry(t, a, network, nil)
				openRegistry(t, b, network, nil)
				connectRegistryPair(t, a, b, id)
				a.registry.Close()
				b.registry.Close()
				network = &registryNetwork{}
				openRegistry(t, a, network, nil)
				openRegistry(t, b, network, nil)
				source, err := a.app.AttachmentStore(id)
				if err != nil {
					t.Fatal(err)
				}
				body := bytes.Repeat([]byte("synthetic paused encrypted object"), 4000)
				o := transfer.Offer{ID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{203}, 16)), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
				if err := source.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
					t.Fatal(err)
				}
				gated := &gatedSourceRepository{Repository: source, started: make(chan struct{}, 1), release: make(chan struct{})}
				engine, err := transfer.New(gated, transfer.Options{})
				if err != nil {
					t.Fatal(err)
				}
				manager, _ := a.registry.manager(id)
				manager.transfers = engine
				connectRegistryPair(t, a, b, id)
				if err := a.registry.QueueTransfer(id, o.ID, false); err != nil {
					t.Fatal(err)
				}
				received, err := b.app.AttachmentStore(id)
				if err != nil {
					t.Fatal(err)
				}
				until := time.Now().Add(3 * time.Second)
				for {
					r, err := received.Get(o.ID)
					if err == nil && r.State == transfer.Offered {
						break
					}
					if time.Now().After(until) {
						t.Fatal("offer not persisted")
					}
					time.Sleep(time.Millisecond)
				}
				if err := b.registry.AcceptTransfer(id, o.ID); err != nil {
					t.Fatal(err)
				}
				select {
				case <-gated.started:
				case <-time.After(time.Second):
					t.Fatal("file did not reach accepted state")
				}
				if err := a.registry.CancelTransfer(id, o.ID); err != nil {
					t.Fatal(err)
				}
				// Both devices now reopen. No per-session offer map survives.
				reopenWithdrawPeer(t, a)
				reopenWithdrawPeer(t, b)
				network = &registryNetwork{}
				openRegistry(t, a, network, nil)
				openRegistry(t, b, network, nil)
				if mode == "reconnected-cancel" {
					connectRegistryPair(t, a, b, id)
				}
				local := a
				if actor == "receiver" {
					local = b
				}
				if mode == "offline-clear" {
					_, err = local.registry.ClearHistory(id)
				} else {
					err = local.registry.WithdrawTransfer(id, o.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if mode != "reconnected-cancel" {
					// Persist intent a second time across an app restart before
					// the peer becomes available. This is not an in-memory retry.
					reopenWithdrawPeer(t, a)
					reopenWithdrawPeer(t, b)
					network = &registryNetwork{}
					openRegistry(t, a, network, nil)
					openRegistry(t, b, network, nil)
					connectRegistryPair(t, a, b, id)
				}
				until = time.Now().Add(4 * time.Second)
				for {
					left, _ := a.app.AttachmentStore(id)
					right, _ := b.app.AttachmentStore(id)
					ar, ae := left.Get(o.ID)
					br, be := right.Get(o.ID)
					if ae == nil && be == nil && ar.State == transfer.Declined && br.State == transfer.Declined && !ar.PendingWithdrawal && !br.PendingWithdrawal {
						break
					}
					if time.Now().After(until) {
						t.Fatalf("durable cancellation did not retire both sides: %s/%s pending=%t/%t", ar.State, br.State, ar.PendingWithdrawal, br.PendingWithdrawal)
					}
					time.Sleep(2 * time.Millisecond)
				}
				for _, peer := range []*registryPeer{a, b} {
					scope, err := peer.app.AttachmentStore(id)
					if err != nil {
						t.Fatal(err)
					}
					active, err := scope.List()
					if err != nil || len(active) != 0 {
						t.Fatal("cancel retained a transfer slot", err)
					}
				}
				if _, err := a.registry.Send(id, "synthetic text after durable cancellation"); err != nil {
					t.Fatal(err)
				}
				waitRegistry(t, a, id, func(s Snapshot) bool { return len(s.Messages) == 1 && s.Messages[0].Delivered })
				waitRegistry(t, b, id, func(s Snapshot) bool { return len(s.Messages) == 1 })
			})
		}
	}
}
