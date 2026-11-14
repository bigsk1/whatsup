package messaging

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"

	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
)

func connectControlPair(t *testing.T, a, b *registryPeer, n *registryNetwork, id string) {
	t.Helper()
	if _, err := a.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for {
		n.mu.Lock()
		ready := n.listeners[id] != nil
		n.mu.Unlock()
		if ready {
			break
		}
		if time.Now().After(until) {
			t.Fatal("synthetic listener not ready")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := b.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*registryPeer{a, b} {
		waitRegistry(t, p, id, func(s Snapshot) bool { return s.State == "connected" && s.ContactControlsSupported })
	}
	for _, p := range []*registryPeer{a, b} {
		m, err := p.registry.manager(id)
		if err != nil {
			t.Fatal(err)
		}
		until = time.Now().Add(time.Second)
		for {
			m.mu.Lock()
			ready := m.controls != nil
			m.mu.Unlock()
			if ready {
				break
			}
			if time.Now().After(until) {
				t.Fatal("control writer not ready")
			}
			time.Sleep(time.Millisecond)
		}
	}
}

func TestOnlineRemovalReceiptIsDurableAndOtherContactsRemainIndependent(t *testing.T) {
	a, b, c := newRegistryPeer(t), newRegistryPeer(t), newRegistryPeer(t)
	ab, ac := seedRegistryAssociation(t, a, b), seedRegistryAssociation(t, a, c)
	n := &registryNetwork{}
	for _, p := range []*registryPeer{a, b, c} {
		openRegistry(t, p, n, nil)
	}
	connectControlPair(t, a, b, n, ab)
	connectControlPair(t, a, c, n, ac)
	if _, err := a.registry.Send(ab, "synthetic history retained after peer removal"); err != nil {
		t.Fatal(err)
	}
	before := waitRegistry(t, b, ab, func(s Snapshot) bool { return len(s.Messages) == 1 })
	if receipt, err := a.registry.NotifyRemoved(context.Background(), ab); err != nil || !receipt {
		t.Fatal("online removal did not receive durable receipt", err)
	}
	removed := waitRegistry(t, b, ab, func(s Snapshot) bool { return s.State == "peer-removed" })
	if removed.CanConnect || removed.CanSend || len(removed.Messages) != 1 || removed.Messages[0].ID != before.Messages[0].ID {
		t.Fatal("remote removal lost history or retained network authority")
	}
	if _, err := b.registry.Connect(ab); err == nil {
		t.Fatal("removed peer reconnected")
	}
	if _, err := b.registry.Send(ab, "must not queue"); err == nil {
		t.Fatal("removed peer queued text")
	}
	records, err := a.app.CommittedContacts()
	if err != nil {
		t.Fatal(err)
	}
	var forgotten storage.PendingRecord
	for _, r := range records {
		if r.ID == ab {
			forgotten = r
		}
	}
	if err := a.app.ForgetCommittedContact(forgotten, func() error { return a.registry.Retire(ab) }); err != nil {
		t.Fatal("local Forget depended on remote state", err)
	}
	if _, err := a.registry.Send(ac, "synthetic independent contact still connected"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, c, ac, func(s Snapshot) bool {
		return len(s.Messages) == 1 && s.Messages[0].Text == "synthetic independent contact still connected"
	})
	// Cold reopen uses the encrypted marker, preserving pins and local history.
	b.registry.Close()
	if err := b.app.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err := storage.Open(b.directory, b.keys)
	if err != nil {
		t.Fatal(err)
	}
	b.app, err = storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	b.local, err = b.app.Identity()
	if err != nil {
		t.Fatal(err)
	}
	openRegistry(t, b, n, nil)
	restored := b.registry.Snapshot(ab)
	if restored.State != "peer-removed" || restored.CanSend || restored.CanConnect || len(restored.Messages) != 1 || restored.PeerFingerprint != before.PeerFingerprint {
		t.Fatal("restart revived removed peer or lost its identity/history")
	}
	if _, err := b.registry.Connect(ab); err == nil {
		t.Fatal("cold removed contact could connect")
	}
}

func TestRemovalOfflineDoesNotCreateRuntimeOrStartNetwork(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	n := &registryNetwork{}
	openRegistry(t, a, n, nil)
	for _, target := range []string{id, capacityID(9999)} {
		if receipt, err := a.registry.NotifyRemoved(context.Background(), target); err != nil || receipt {
			t.Fatal("offline notice reported delivery", err)
		}
	}
	n.mu.Lock()
	calls := len(n.calls)
	n.mu.Unlock()
	if calls != 0 {
		t.Fatal("offline notice started networking")
	}
	if a.registry.Snapshot(id).State != "offline" {
		t.Fatal("offline notice changed durable/runtime approval")
	}
}

func TestControlEnvelopeRejectsMalformedUnknownAndTruncatedBeforeBody(t *testing.T) {
	good, err := encodeControl(controlFrame{kind: removedControl, id: capacityID(1)})
	if err != nil {
		t.Fatal(err)
	}
	for length := 1; length < len(good); length++ {
		if _, err := readPacket(bytes.NewReader(good[:length])); err == nil {
			t.Fatal("partial control accepted")
		}
	}
	for _, mutate := range []func([]byte){func(b []byte) { b[1] = 0 }, func(b []byte) { b[1] = 16 }, func(b []byte) { clear(b[2:18]) }, func(b []byte) { binary.BigEndian.PutUint16(b[18:], 1) }, func(b []byte) { binary.BigEndian.PutUint16(b[18:], ^uint16(0)) }} {
		wire := bytes.Clone(good)
		mutate(wire)
		reader := &headerOnlyReader{header: bytes.NewReader(wire)}
		if _, err := readPacket(reader); !errors.Is(err, errProtocol) || reader.bodyRead {
			t.Fatal("malformed control was accepted or read body")
		}
	}
	var wire bytes.Buffer
	wire.Write(good)
	wire.WriteByte(pingPacket)
	wire.Write(make([]byte, 16))
	first, err := readPacket(&wire)
	if err != nil || first.control.id != capacityID(1) {
		t.Fatal(err)
	}
	second, err := readPacket(&wire)
	if err != nil || second.kind != pingPacket || wire.Len() != 0 {
		t.Fatal("control swallowed next packet")
	}
}

func TestUnsolicitedOrOtherGenerationRemovalReceiptCannotPauseContact(t *testing.T) {
	for _, kind := range []byte{removedReceiptControl, removedControl} {
		t.Run(map[byte]string{removedReceiptControl: "unsolicited-receipt", removedControl: "invalid-contact-authority"}[kind], func(t *testing.T) {
			m, server, client, conversation, _ := exchangeFixture(t)
			done := make(chan error, 1)
			go func() { done <- m.exchange(context.Background(), server, conversation) }()
			consumeInitialProfile(t, client)
			if err := writePacket(client, packet{kind: controlPacket, control: controlFrame{kind: kind, id: capacityID(123)}}); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("forged/unsolicited control succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("invalid control kept exchange alive")
			}
			if m.peerRemoved {
				t.Fatal("uncommitted control changed runtime removal")
			}
		})
	}
}

func currentControlConnection(t *testing.T, beforeProfile ...func(*Manager, *sessionauth.Session)) (*Manager, *sessionauth.Session, <-chan error) {
	t.Helper()
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	m, err := NewContact(a.local, a.app, id)
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done, finished := make(chan error, 1), make(chan struct{})
	go func() { defer close(finished); done <- m.session(left) }()
	peer, err := sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: b.local, Peer: a.local.PublicKey(), ContactID: id, Association: binding})
	if err != nil {
		cancel()
		_ = left.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = peer.Close()
		_ = left.Close()
		m.Close()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("raw control session did not join")
		}
	})
	for _, before := range beforeProfile {
		before(m, peer)
	}
	consumeInitialProfile(t, peer)
	if len(beforeProfile) == 0 {
		// A real heartbeat round trip is a wire barrier after startup sync, so
		// persistence-failure tests exercise receipt handling rather than racing
		// unrelated initial metadata reads.
		nonce := [16]byte{77}
		if err := writePacket(peer, packet{kind: pingPacket, nonce: nonce}); err != nil {
			t.Fatal(err)
		}
		pong, err := readPacket(peer)
		if err != nil || pong.kind != pongPacket || pong.nonce != nonce {
			t.Fatal("control fixture did not reach writer loop", err)
		}
	}
	return m, peer, done
}

func TestImmediateForgetYieldsStartupRelaySyncToPriorityNotice(t *testing.T) {
	result := make(chan bool, 1)
	_, peer, done := currentControlConnection(t, func(m *Manager, _ *sessionauth.Session) {
		limit := time.Now().Add(time.Second)
		for {
			m.mu.Lock()
			ready := m.sessionActive && m.controls != nil
			m.mu.Unlock()
			if ready {
				break
			}
			if time.Now().After(limit) {
				t.Fatal("authenticated writer not published")
			}
			time.Sleep(time.Millisecond)
		}
		go func() { receipt, _ := m.notifyRemoved(context.Background(), m.record.ID); result <- receipt }()
		for {
			m.mu.Lock()
			removing := m.removing
			m.mu.Unlock()
			if removing {
				break
			}
			if time.Now().After(limit) {
				t.Fatal("Forget did not enqueue before initial profile read")
			}
			time.Sleep(time.Millisecond)
		}
	})
	notice, err := readPacket(peer)
	if err != nil || notice.kind != controlPacket || notice.control.kind != removedControl {
		t.Fatal("startup relay sync discarded priority Forget", err)
	}
	if err := writePacket(peer, packet{kind: controlPacket, control: controlFrame{kind: removedReceiptControl, id: notice.control.id}}); err != nil {
		t.Fatal(err)
	}
	if receipt := <-result; !receipt {
		t.Fatal("immediate Forget lost authenticated receipt")
	}
	if err := <-done; !errors.Is(err, errRemovalSent) {
		t.Fatal(err)
	}
}

func TestLostRemovalReceiptCannotRevivePeerAfterReopen(t *testing.T) {
	m, peer, done := currentControlConnection(t)
	id := capacityID(808)
	if err := writePacket(peer, packet{kind: controlPacket, control: controlFrame{kind: removedControl, id: id}}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for {
		saved, err := m.application.PeerRemoval(m.record)
		if err == nil && saved == id {
			break
		}
		if time.Now().After(until) {
			t.Fatal("marker was not committed before blocked receipt", err)
		}
		time.Sleep(time.Millisecond)
	}
	// The peer never reads the receipt; closing it models receipt loss.
	_ = peer.Close()
	select {
	case err := <-done:
		if !errors.Is(err, errPeerRemoved) {
			t.Fatal("lost receipt changed terminal state", err)
		}
	case <-time.After(time.Second):
		t.Fatal("lost receipt blocked session close")
	}
	m.Close()
	reopened, err := NewContact(m.local, m.application, m.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	snapshot := reopened.Snapshot()
	if snapshot.State != "peer-removed" || snapshot.CanConnect || snapshot.CanSend {
		t.Fatal("lost receipt revived target authority")
	}
	if saved, err := m.application.PeerRemoval(m.record); err != nil || saved != id {
		t.Fatal("terminal ID changed after replay", err)
	}
	if err := m.application.MarkPeerRemoved(m.record, id); err != nil {
		t.Fatal("duplicate terminal notice was not idempotent", err)
	}
}

func TestFailedRemovalPersistenceSendsNoReceipt(t *testing.T) {
	m, peer, done := currentControlConnection(t)
	if err := m.application.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writePacket(peer, packet{kind: controlPacket, control: controlFrame{kind: removedControl, id: capacityID(809)}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed storage accepted removal")
		}
	case <-time.After(time.Second):
		t.Fatal("failed persistence did not terminate")
	}
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	var first [1]byte
	if n, err := peer.Read(first[:]); n != 0 || err == nil {
		t.Fatal("failed durable removal sent an acknowledgement")
	}
	m.mu.Lock()
	removed := m.peerRemoved
	m.mu.Unlock()
	if removed {
		t.Fatal("failed persistence changed runtime marker")
	}
}

func TestRemovalWaitIsBoundedWhenAuthenticatedPeerStopsReading(t *testing.T) {
	m, peer, done := currentControlConnection(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	if receipt, err := m.notifyRemoved(ctx, m.record.ID); err != nil || receipt {
		t.Fatal("nonreading peer claimed receipt", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatal("notice ignored caller deadline")
	}
	_ = peer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled notice failed to join")
	}
}

func TestReplayedRemovalReceiptCannotAcknowledgeAnotherRequest(t *testing.T) {
	m, peer, done := currentControlConnection(t)
	result := make(chan bool, 1)
	go func() { receipt, _ := m.notifyRemoved(context.Background(), m.record.ID); result <- receipt }()
	request, err := readPacket(peer)
	if err != nil || request.kind != controlPacket || request.control.kind != removedControl {
		t.Fatal("removal was not sent", err)
	}
	oldID := capacityID(810)
	if oldID == request.control.id {
		t.Fatal("synthetic ID unexpectedly collided")
	}
	if err := writePacket(peer, packet{kind: controlPacket, control: controlFrame{kind: removedReceiptControl, id: oldID}}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errProtocol) {
			t.Fatal("replayed receipt was not rejected", err)
		}
	case <-time.After(time.Second):
		t.Fatal("replayed receipt stalled exchange")
	}
	if receipt := <-result; receipt {
		t.Fatal("another generation/request receipt claimed delivery")
	}
	m.mu.Lock()
	removed := m.peerRemoved
	m.mu.Unlock()
	if removed {
		t.Fatal("receipt was treated as a peer removal notice")
	}
}
