package messaging

import (
	"bytes"
	"crypto/sha256"
	"testing"
	"time"

	"whatsup/internal/transfer"
)

func TestDeleteOfflineQueuedAttachmentReclaimsPeerSlotAndCannotResendAfterReconnect(t *testing.T) {
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
	destination, err := b.app.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic single offline deletion")
	o := transfer.Offer{ID: capacityID(27101), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
	if err := source.Stage(t.Context(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := source.QueueOutgoing(o.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := destination.Offer(o); err != nil {
		t.Fatal(err)
	}
	if _, err := destination.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.RemoveTransfer(id, o.ID); err != nil {
		t.Fatal(err)
	}
	if record, _ := source.Get(o.ID); !record.Hidden || record.State != transfer.Declined || !record.PendingWithdrawal {
		t.Fatal("offline delete lost durable withdrawal")
	}
	connectRegistryPair(t, a, b, id)
	until := time.Now().Add(5 * time.Second)
	for {
		left, le := source.Get(o.ID)
		right, re := destination.Get(o.ID)
		if le == nil && re == nil && !left.PendingWithdrawal && right.State == transfer.Declined {
			break
		}
		if time.Now().After(until) {
			t.Fatal("delete did not settle cancellation on the same authenticated stream", le, re)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := a.registry.Send(id, "synthetic text after one deletion"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, a, id, func(s Snapshot) bool { return len(s.Messages) == 1 && s.Messages[0].Delivered })
	a.registry.Close()
	b.registry.Close()
	network = &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	connectRegistryPair(t, a, b, id)
	if err := a.registry.RemoveTransfer(id, o.ID); err != nil {
		t.Fatal("reopened deletion is not idempotent", err)
	}
	if list, err := a.registry.Transfers(id); err != nil || len(list) != 0 {
		t.Fatal("deleted sender returned after reconnect", err)
	}
	if record, err := destination.Get(o.ID); err != nil || record.State != transfer.Declined {
		t.Fatal("receiver's canceled object resumed", err)
	}
	if _, err := a.registry.Send(id, "synthetic text after deletion reconnect"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, a, id, func(s Snapshot) bool {
		return len(s.Messages) == 2 && s.Messages[0].Delivered && s.Messages[1].Delivered
	})
}
