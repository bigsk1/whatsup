package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/transfer"
)

type pacedWithdrawalRepository struct {
	transfer.Repository
	committed *atomic.Int64
	changed   chan<- struct{}
}

func (r pacedWithdrawalRepository) AcknowledgeWithdrawal(id string, completed bool) error {
	// Keep the real encrypted SQLite transaction, with a small local stall so
	// a fast TLS peer cannot hide a queue-capacity regression behind fast disks.
	time.Sleep(3 * time.Millisecond)
	err := r.Repository.AcknowledgeWithdrawal(id, completed)
	if err == nil {
		r.committed.Add(1)
		select {
		case r.changed <- struct{}{}:
		default:
		}
	}
	return err
}

func TestSimultaneousLargeCancellationReplayUsesOneTLSConnectionAndKeepsTextLive(t *testing.T) {
	started := time.Now()
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	connectRegistryPair(t, a, b, id)
	a.registry.Close()
	b.registry.Close()
	left, _ := a.app.AttachmentStore(id)
	right, _ := b.app.AttachmentStore(id)
	const count = transfer.MaxOffers + 17 // More than both the 32-event and 128-page bounds.
	for index := 1; index <= count; index++ {
		for side, pair := range [][2]*registryPeer{{a, b}, {b, a}} {
			var raw [16]byte
			raw[0] = byte(side + 1)
			binary.BigEndian.PutUint64(raw[8:], uint64(index))
			body := []byte("synthetic replay body")
			o := transfer.Offer{ID: base64.RawURLEncoding.EncodeToString(raw[:]), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
			source, err := pair[0].app.AttachmentStore(id)
			if err != nil {
				t.Fatal(err)
			}
			destination, err := pair[1].app.AttachmentStore(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := source.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
				t.Fatal(err)
			}
			if err := source.QueueOutgoing(o.ID, false); err != nil {
				t.Fatal(err)
			}
			if err := destination.Offer(o); err != nil {
				t.Fatal(err)
			}
			if err := destination.Decline(o.ID); err != nil {
				t.Fatal(err)
			}
			if err := source.Withdraw(o.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	network = &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	var committed [2]atomic.Int64
	changed := make(chan struct{}, 1)
	for index, peer := range []*registryPeer{a, b} {
		m, err := peer.registry.manager(id)
		if err != nil {
			t.Fatal(err)
		}
		scope, _ := peer.app.AttachmentStore(id)
		m.transfers, err = transfer.New(pacedWithdrawalRepository{Repository: scope, committed: &committed[index], changed: changed}, transfer.Options{})
		if err != nil {
			t.Fatal(err)
		}
	}
	prepared := time.Now()
	connectRegistryPair(t, a, b, id)
	for _, peer := range []*registryPeer{a, b} {
		if _, err := peer.registry.Send(id, "synthetic text during cancellation replay"); err != nil {
			t.Fatal(err)
		}
	}
	// Observe only completed real transactions. Repeated List calls decrypt the
	// entire runtime set under the same store mutex and can starve the actors,
	// especially with the race detector and software SQLite instrumentation.
	// Race-instrumented pure-Go SQLite measured48 commits/side in15s at the
	// largest pending-set size. Ninety seconds bounds liveness with headroom
	// over that conservative full-backlog projection, without imposing a
	// storage-throughput SLA or weakening the exact completion assertions.
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	for committed[0].Load() < count || committed[1].Load() < count {
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("cancellation replay did not drain: committed=%d/%d of %d each (fixture=%s replay=%s)", committed[0].Load(), committed[1].Load(), count, prepared.Sub(started), time.Since(prepared))
		}
	}
	t.Logf("actual SQLite/Tink fixture=%s, TLS replay=%s, committed=%d/%d", prepared.Sub(started), time.Since(prepared), committed[0].Load(), committed[1].Load())
	if committed[0].Load() != count || committed[1].Load() != count {
		t.Fatal("receipt counters did not match exact durable intents")
	}
	ar, ae := left.List()
	br, be := right.List()
	if ae != nil || be != nil || len(ar) != 0 || len(br) != 0 {
		t.Fatalf("final encrypted cancellation state retained work: %d/%d errors=%v/%v", len(ar), len(br), ae, be)
	}
	for _, peer := range []*registryPeer{a, b} {
		waitRegistry(t, peer, id, func(s Snapshot) bool {
			return len(s.Messages) == 2 && s.Messages[0].Delivered && s.Messages[1].Delivered
		})
		m, _ := peer.registry.manager(id)
		m.mu.Lock()
		authenticated, connected := m.authenticated, m.sessionActive
		m.mu.Unlock()
		if authenticated != 1 || !connected {
			t.Fatal("legitimate cancellation backlog restarted or closed its TLS session", authenticated, connected)
		}
	}
}
