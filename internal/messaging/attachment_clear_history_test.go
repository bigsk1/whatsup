package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"whatsup/internal/storage"
	"whatsup/internal/transfer"
)

func TestOfflineClearWithdrawsQueuedFilesAcrossColdRestart(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	connectRegistryPair(t, a, b, id)
	a.registry.Close()
	b.registry.Close()
	openRegistry(t, a, &registryNetwork{}, nil)
	source, err := a.app.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic queued attachment cleared while offline")
	o := transfer.Offer{ID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{89}, 16)), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
	if err := source.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := a.registry.QueueTransfer(id, o.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.registry.ClearHistory(id); err != nil {
		t.Fatal(err)
	}
	a.registry.Close()
	if err := a.app.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err := storage.Open(a.directory, a.keys)
	if err != nil {
		t.Fatal(err)
	}
	a.app, err = storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	a.local, err = a.app.Identity()
	if err != nil {
		t.Fatal(err)
	}
	network = &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	connectRegistryPair(t, a, b, id)
	if _, err := a.registry.Send(id, "synthetic text after cleared attachment restart"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, a, id, func(s Snapshot) bool { return len(s.Messages) == 1 && s.Messages[0].Delivered })
	waitRegistry(t, b, id, func(s Snapshot) bool { return len(s.Messages) == 1 })
	remote, err := b.app.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Get(o.ID); err == nil {
		t.Fatal("cold restart re-offered a cleared queued attachment")
	}
	source, err = a.app.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := source.Get(o.ID)
	if err != nil || !r.Hidden || r.State != transfer.Declined {
		t.Fatal("cold restart revived a cleared queued attachment", err)
	}
}
