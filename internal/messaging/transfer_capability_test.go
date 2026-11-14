package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"

	"whatsup/internal/sessionauth"
	"whatsup/internal/transfer"
)

func TestRestrictedPeerKeepsLocalAttachmentSummariesAndRetirement(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(map[bool]string{false: "protocol-three", true: "peer-removed"}[removed], func(t *testing.T) {
			a, b := newRegistryPeer(t), newRegistryPeer(t)
			id := seedRegistryAssociation(t, a, b)
			network := &routeTestNetwork{}
			openRouteRegistry(t, a, network, "a")
			openRouteRegistry(t, b, network, "b")
			connectRoutePair(t, a, b, network, id)
			a.registry.Close()
			b.registry.Close()
			openRouteRegistry(t, a, network, "a-local")
			m, err := a.registry.manager(id)
			if err != nil {
				t.Fatal(err)
			}
			scope, err := a.app.AttachmentStore(id)
			if err != nil {
				t.Fatal(err)
			}
			data := []byte("synthetic local attachment retained")
			out := transfer.Offer{ID: capacityID(1701), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(data)), SHA256: sha256.Sum256(data)}
			if err := scope.Stage(context.Background(), out, bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			if err := scope.QueueOutgoing(out.ID, true); err != nil {
				t.Fatal(err)
			}
			incoming := out
			incoming.ID = capacityID(1702)
			if err := scope.Offer(incoming); err != nil {
				t.Fatal(err)
			}
			if removed {
				records, _ := a.app.CommittedContacts()
				if err := a.app.MarkPeerRemoved(records[0], capacityID(1703)); err != nil {
					t.Fatal(err)
				}
			} else {
				m.mu.Lock()
				m.sessionActive = true
				m.capabilities = sessionauth.Capabilities{WireVersion: 3}
				m.mu.Unlock()
			}
			values, err := a.registry.Transfers(id)
			if err != nil || len(values) != 2 {
				t.Fatal("restricted peer hid local attachments", err)
			}
			if err := a.registry.QueueTransfer(id, out.ID, true); err == nil {
				t.Fatal("restricted peer queued a transfer")
			}
			if err := a.registry.AcceptTransfer(id, incoming.ID); err == nil {
				t.Fatal("restricted peer accepted a transfer")
			}
			if err := a.registry.AllowTransferRelay(id, out.ID); err == nil {
				t.Fatal("restricted peer changed network permission")
			}
			if err := a.registry.CancelTransfer(id, out.ID); err != nil {
				t.Fatal("restricted peer could not cancel locally", err)
			}
			if err := a.registry.ResumeTransfer(id, out.ID); err == nil {
				t.Fatal("restricted peer resumed a transfer")
			}
			if err := a.registry.DeclineTransfer(id, incoming.ID); err != nil {
				t.Fatal("restricted peer could not decline locally", err)
			}
			if err := a.registry.WakeTransfers(id); err != nil {
				t.Fatal("local metadata hint required network authority", err)
			}
			m.mu.Lock()
			created := m.transfers != nil
			m.mu.Unlock()
			if created {
				t.Fatal("local attachment lookup started a transfer actor")
			}
		})
	}
}
