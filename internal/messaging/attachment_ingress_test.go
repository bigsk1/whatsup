package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"

	"whatsup/internal/chat"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transfer"
)

// A slow local file read blocks the real attachment actor while a compatible
// peer's cancellation replay fills its32-event input. Text and heartbeat must
// retain their independent scheduler for longer than the12-second peer timeout.
func TestSlowAttachmentActorDoesNotBlockInboundTextOrHeartbeats(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	connectRegistryPair(t, a, b, id)
	a.registry.Close()
	b.registry.Close()
	m, err := NewContact(a.local, a.app, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	source, err := a.app.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic gated attachment")
	o := transfer.Offer{ID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{232}, 16)), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
	if err := source.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	gated := &gatedSourceRepository{Repository: source, started: make(chan struct{}, 1), release: release}
	m.transfers, err = transfer.New(gated, transfer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	records, err := b.app.CommittedContacts()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := storage.ContactBinding(records[0])
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); done <- m.session(left) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: b.local, Peer: a.local.PublicKey(), ContactID: id, Association: binding})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = peer.Close()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("slow attachment session failed to join")
		}
	})
	consumeInitialProfile(t, peer)
	if err := m.transfers.Queue(o.ID, false); err != nil {
		t.Fatal(err)
	}
	offered, err := readPacket(peer)
	if err != nil || offered.kind != transferPacket || offered.attachment.Kind != transfer.OfferFrame {
		t.Fatal("missing outgoing offer", err)
	}
	if err := writePacket(peer, packetForAccept(o)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gated.started:
	case <-time.After(time.Second):
		t.Fatal("actor did not enter slow file read")
	}
	var writeMu sync.Mutex
	write := func(p packet) error { writeMu.Lock(); defer writeMu.Unlock(); return writePacket(peer, p) }
	pongs, acks := make(chan struct{}, 8), make(chan string, 8)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			p, err := readPacket(peer)
			if err != nil {
				return
			}
			switch p.kind {
			case pingPacket:
				if write(packet{kind: pongPacket, nonce: p.nonce}) != nil {
					return
				}
			case pongPacket:
				pongs <- struct{}{}
			case dataPacket:
				if p.frame.Type == chat.AckFrame {
					acks <- p.frame.ID
				}
			}
		}
	}()
	t.Cleanup(func() { _ = peer.Close(); <-readerDone })
	// This is below the supported old replay bound128, but above actor capacity32.
	for index := 0; index < 48; index++ {
		var raw [16]byte
		raw[0] = 233
		binary.BigEndian.PutUint64(raw[8:], uint64(index+1))
		f := transfer.Frame{Kind: transfer.WithdrawFrame, ID: base64.RawURLEncoding.EncodeToString(raw[:]), SHA256: sha256.Sum256([]byte("synthetic replay offer"))}
		if err := write(packet{kind: transferPacket, attachment: f}); err != nil {
			t.Fatal(err)
		}
	}
	if err := write(packet{kind: pingPacket, nonce: [16]byte{234}}); err != nil {
		t.Fatal(err)
	}
	messageID := capacityID(23001)
	if err := write(packet{kind: dataPacket, frame: chat.Frame{Version: 1, Type: chat.MessageFrame, ID: messageID, Text: "synthetic inbound text during slow attachment"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pongs:
	case <-time.After(time.Second):
		t.Fatal("slow actor blocked an inbound keepalive")
	}
	select {
	case got := <-acks:
		if got != messageID {
			t.Fatal("wrong text acknowledged")
		}
	case <-time.After(time.Second):
		t.Fatal("slow actor blocked inbound text ACK")
	}
	timer := time.NewTimer(peerTimeout + time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case err := <-done:
		t.Fatal("slow actor killed the live session", err)
	}
	if err := write(packet{kind: pingPacket, nonce: [16]byte{235}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pongs:
	case <-time.After(time.Second):
		t.Fatal("heartbeat scheduler stalled while actor stayed blocked")
	}
	// The gate is still held. Remote EOF must cancel the actor's session work
	// before joining End, instead of waiting for an app-wide shutdown or file IO.
	_ = peer.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("remote close deadlocked behind a slow attachment source")
	}
}

func packetForAccept(offer transfer.Offer) packet {
	return packet{kind: transferPacket, attachment: transfer.Frame{Kind: transfer.AcceptFrame, ID: offer.ID, SHA256: offer.SHA256, Credit: [16]byte{1}}}
}

func TestAttachmentIngressBoundsChunksAndSmallControlsSeparately(t *testing.T) {
	for _, chunks := range []bool{true, false} {
		t.Run(map[bool]string{true: "chunk bytes", false: "small controls"}[chunks], func(t *testing.T) {
			q := &attachmentIngress{ctx: context.Background(), events: make(chan attachmentIngressEvent, maxAttachmentIngressChunks+maxAttachmentIngressControls)}
			o := transfer.Offer{ID: capacityID(24001), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: transfer.ChunkBytes, SHA256: sha256.Sum256([]byte("synthetic ingress bound"))}
			frame := transfer.Frame{Kind: transfer.WithdrawFrame, ID: o.ID, SHA256: o.SHA256}
			limit := maxAttachmentIngressControls
			if chunks {
				frame = transfer.Frame{Kind: transfer.ChunkFrame, ID: o.ID, SHA256: o.SHA256, Data: make([]byte, transfer.ChunkBytes), Credit: [16]byte{1}}
				limit = maxAttachmentIngressChunks
			}
			for range limit {
				if err := q.submit(frame); err != nil {
					t.Fatal("supported bounded window rejected", err)
				}
			}
			if err := q.submit(frame); err == nil {
				t.Fatal("peer flood exceeded the bounded ingress allowance")
			}
			if q.chunks > maxAttachmentIngressChunks || q.controls > maxAttachmentIngressControls || len(q.events) != limit {
				t.Fatal("rejected flood retained additional state")
			}
		})
	}
}

func TestLostWithdrawalReceiptRetriesWithinSameSQLiteTLSConnection(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	network := &registryNetwork{}
	openRegistry(t, a, network, nil)
	openRegistry(t, b, network, nil)
	connectRegistryPair(t, a, b, id)
	a.registry.Close()
	b.registry.Close()
	m, err := NewContact(a.local, a.app, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	source, err := a.app.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	destination, err := b.app.AttachmentStore(id)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic durable lost receipt")
	o := transfer.Offer{ID: capacityID(24002), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
	if err := source.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
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
	if err := source.Withdraw(o.ID); err != nil {
		t.Fatal(err)
	}
	records, err := b.app.CommittedContacts()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := storage.ContactBinding(records[0])
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- m.session(left) }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	peer, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: b.local, Peer: a.local.PublicKey(), ContactID: id, Association: binding})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = peer.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("lost-receipt session failed to join")
		}
	})
	consumeInitialProfile(t, peer)
	_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
	for attempt := 0; attempt < 2; attempt++ {
		p, err := readPacket(peer)
		if err != nil || p.kind != transferPacket || p.attachment.Kind != transfer.WithdrawFrame || p.attachment.ID != o.ID || p.attachment.SHA256 != o.SHA256 {
			t.Fatal("withdrawal did not retry on the existing authenticated stream", err)
		}
		if attempt == 0 {
			if err := destination.Decline(o.ID); err != nil {
				t.Fatal(err)
			}
			// Simulate a peer dropping its queued receipt while remaining online.
			continue
		}
		if record, err := destination.Get(o.ID); err != nil || record.State != transfer.Declined {
			t.Fatal("repeated cancellation revived the receiver", err)
		}
		if err := writePacket(peer, packet{kind: transferPacket, attachment: transfer.Frame{Kind: transfer.WithdrawAckFrame, ID: o.ID, SHA256: o.SHA256, Reason: transfer.ReasonDeclined}}); err != nil {
			t.Fatal(err)
		}
	}
	until := time.Now().Add(time.Second)
	for {
		record, err := source.Get(o.ID)
		if err == nil && !record.PendingWithdrawal {
			break
		}
		if time.Now().After(until) {
			t.Fatal("retried durable receipt did not release the sender slot")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := m.Send(id, "synthetic text after live cancellation retry"); err != nil {
		t.Fatal(err)
	}
	p, err := readPacket(peer)
	if err != nil || p.kind != dataPacket || p.frame.Type != chat.MessageFrame {
		t.Fatal("text did not survive retry", err)
	}
	if err := writePacket(peer, packet{kind: dataPacket, frame: chat.Frame{Version: 1, Type: chat.AckFrame, ID: p.frame.ID}}); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(time.Second)
	for len(m.conversation.Outbox()) != 0 {
		if time.Now().After(until) {
			t.Fatal("post-retry text was not acknowledged")
		}
		time.Sleep(time.Millisecond)
	}
	m.mu.Lock()
	auth, active := m.authenticated, m.sessionActive
	m.mu.Unlock()
	if auth != 1 || !active {
		t.Fatal("lost receipt was resolved by reconnecting instead of live retry")
	}
}
