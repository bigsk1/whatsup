package messaging

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	legacychat "whatsup/internal/messaging/testdata/legacyv3/chat"
	legacyv3 "whatsup/internal/messaging/testdata/legacyv3/sessionauth"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
)

func historicalConnection(t *testing.T, currentServer bool) (*Manager, *legacyv3.Session, <-chan error) {
	t.Helper()
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	current, previous := a, b
	if !currentServer {
		current, previous = b, a
	}
	manager, err := NewContact(current.local, current.app, id)
	if err != nil {
		t.Fatal(err)
	}
	records, err := previous.app.CommittedContacts()
	if err != nil || len(records) != 1 {
		t.Fatal(err)
	}
	record := records[0]
	binding, err := storage.ContactBinding(record)
	if err != nil {
		t.Fatal(err)
	}
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	done, finished := make(chan error, 1), make(chan struct{})
	go func() { defer close(finished); done <- manager.session(left) }()
	previousSession, err := legacyv3.Authenticate(ctx, right, legacyv3.Options{Local: previous.local, Peer: current.local.PublicKey(), ContactID: id, Association: binding, Server: !currentServer})
	if err != nil {
		cancel()
		_ = left.Close()
		t.Fatal("historical TLS implementation failed", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = previousSession.Close()
		_ = left.Close()
		manager.Close()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("historical session failed to join")
		}
	})
	consumeHistoricalProfile(t, previousSession)
	manager.mu.Lock()
	caps, engine := manager.capabilities, manager.transfers
	manager.mu.Unlock()
	if caps != (sessionauth.Capabilities{WireVersion: 3}) || engine != nil {
		t.Fatal("previous protocol enabled current controls or attachment engine")
	}
	return manager, previousSession, done
}

func consumeHistoricalProfile(t *testing.T, session *legacyv3.Session) {
	t.Helper()
	var header [4]byte
	if _, err := io.ReadFull(session, header[:]); err != nil || header[0] != 4 || header[1] != 1 || binary.BigEndian.Uint16(header[2:]) != 0 {
		t.Fatal("historical profile frame changed", err)
	}
}

func historicalWrite(t *testing.T, session *legacyv3.Session, kind byte, frame legacychat.Frame) {
	t.Helper()
	if session.SetWriteDeadline(time.Now().Add(time.Second)) != nil {
		t.Fatal("historical deadline failed")
	}
	if n, err := session.Write([]byte{kind}); err != nil || n != 1 {
		t.Fatal("historical packet header failed", err)
	}
	if err := legacychat.WriteFrame(session, frame); err != nil {
		t.Fatal("historical canonical frame failed", err)
	}
}

func historicalRead(t *testing.T, session *legacyv3.Session) (byte, legacychat.Frame) {
	t.Helper()
	_ = session.SetReadDeadline(time.Now().Add(2 * time.Second))
	var kind [1]byte
	if _, err := io.ReadFull(session, kind[:]); err != nil {
		t.Fatal(err)
	}
	frame, err := legacychat.ReadFrame(session)
	if err != nil {
		t.Fatal("current frame not accepted by historical parser", err)
	}
	return kind[0], frame
}

func TestActualPreviousTLSAndCanonicalFramesExchangeBothDirectionsAndRoles(t *testing.T) {
	for _, server := range []bool{true, false} {
		t.Run(map[bool]string{true: "current-server", false: "current-client"}[server], func(t *testing.T) {
			m, previous, done := historicalConnection(t, server)
			if _, err := m.Send(m.record.ID, "synthetic current to previous 👩‍💻"); err != nil {
				t.Fatal(err)
			}
			kind, out := historicalRead(t, previous)
			if kind != 1 || out.Type != legacychat.MessageFrame || out.Text != "synthetic current to previous 👩‍💻" {
				t.Fatal("previous implementation could not receive current text")
			}
			historicalWrite(t, previous, 1, legacychat.Frame{Version: 1, Type: legacychat.AckFrame, ID: out.ID})
			incoming := legacychat.Frame{Version: 1, Type: legacychat.MessageFrame, ID: capacityID(910), Text: "synthetic previous to current 🏳️‍🌈"}
			historicalWrite(t, previous, 1, incoming)
			kind, ack := historicalRead(t, previous)
			if kind != 1 || ack.Type != legacychat.AckFrame || ack.ID != incoming.ID {
				t.Fatal("current did not acknowledge historical frame")
			}
			until := time.Now().Add(time.Second)
			for {
				snapshot := m.Snapshot()
				if len(snapshot.Messages) == 2 && snapshot.Messages[0].Delivered && snapshot.Messages[1].Text == incoming.Text {
					break
				}
				if time.Now().After(until) {
					t.Fatal("previous ACK/text were not persisted")
				}
				time.Sleep(time.Millisecond)
			}
			// The unmodified previous profile format remains usable.
			name := "Synthetic legacy peer"
			profile := []byte{4, 1}
			profile = binary.BigEndian.AppendUint16(profile, uint16(len(name)))
			profile = append(profile, name...)
			if _, err := previous.Write(profile); err != nil {
				t.Fatal(err)
			}
			until = time.Now().Add(time.Second)
			for m.Snapshot().PeerDisplayName != name {
				if time.Now().After(until) {
					t.Fatal("historical profile was not accepted")
				}
				time.Sleep(time.Millisecond)
			}
			if _, err := m.Send(m.record.ID, "synthetic previous capacity refusal"); err != nil {
				t.Fatal(err)
			}
			_, out = historicalRead(t, previous)
			historicalWrite(t, previous, 5, legacychat.Frame{Version: 1, Type: legacychat.AckFrame, ID: out.ID})
			if err := <-done; !errors.Is(err, errPeerCapacity) {
				t.Fatal("historical capacity refusal was not enforced", err)
			}
			if len(m.conversation.Outbox()) != 1 {
				t.Fatal("capacity refusal falsely delivered queued message")
			}
		})
	}
}

func TestPreviousProtocolRejectsNewKindsBeforeBodyRead(t *testing.T) {
	for _, caps := range []sessionauth.Capabilities{{WireVersion: 3}, {WireVersion: 4, Attachments: true}} {
		for _, kind := range []byte{transferPacket, controlPacket} {
			if kind == transferPacket && caps.Attachments {
				continue
			}
			reader := &headerOnlyReader{header: bytes.NewReader([]byte{kind})}
			if _, err := readPacketFor(reader, caps); !errors.Is(err, errProtocol) || reader.bodyRead {
				t.Fatal("unsupported kind consumed body or was accepted")
			}
		}
	}
}
