package messaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"whatsup/internal/chat"
	legacy "whatsup/internal/messaging/testdata/legacyv4/sessionauth"
	legacytransfer "whatsup/internal/messaging/testdata/legacyv4/transfer"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transfer"
)

// The TLS implementation and attachment parser under testdata/legacyv4 are
// byte-for-byte copies of 0.0.11 commit 3e483ae. Its receiving engine rejects
// DeclineFrame; the fixture checks that historical rule as well as its parser.
func TestCancelAndClearAgainstActual0011TLSDoNotSendUnsupportedWithdrawal(t *testing.T) {
	for _, server := range []bool{true, false} {
		for _, clear := range []bool{true, false} {
			t.Run(map[bool]string{true: "server", false: "client"}[server]+map[bool]string{true: "-clear", false: "-cancel"}[clear], func(t *testing.T) {
				a, b := newRegistryPeer(t), newRegistryPeer(t)
				id := seedRegistryAssociation(t, a, b)
				network := &registryNetwork{}
				openRegistry(t, a, network, nil)
				openRegistry(t, b, network, nil)
				connectRegistryPair(t, a, b, id)
				a.registry.Close()
				b.registry.Close()
				current, old := a, b
				if !server {
					current, old = b, a
				}
				m, err := NewContact(current.local, current.app, id)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(m.Close)
				source, err := current.app.AttachmentStore(id)
				if err != nil {
					t.Fatal(err)
				}
				body := []byte("synthetic cancellation against 0.0.11")
				o := transfer.Offer{ID: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{201}, 16)), Name: "synthetic.bin", MediaType: "application/octet-stream", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
				if err := source.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
					t.Fatal(err)
				}
				gated := &gatedSourceRepository{Repository: source, started: make(chan struct{}, 1), release: make(chan struct{})}
				engine, err := transfer.New(gated, transfer.Options{})
				if err != nil {
					t.Fatal(err)
				}
				m.transfers = engine
				records, _ := old.app.CommittedContacts()
				binding, err := storage.ContactBinding(records[0])
				if err != nil {
					t.Fatal(err)
				}
				left, right := net.Pipe()
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				t.Cleanup(cancel)
				done := make(chan error, 1)
				go func() { done <- m.session(left) }()
				peer, err := legacy.Authenticate(ctx, right, legacy.Options{Local: old.local, Peer: current.local.PublicKey(), ContactID: id, Association: binding, Server: !server})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = peer.Close()
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("historical stream did not join")
					}
				})
				_ = peer.SetReadDeadline(time.Now().Add(3 * time.Second))
				var header [4]byte
				if _, err := io.ReadFull(peer, header[:]); err != nil || header[0] != profilePacket || header[1] != 1 || binary.BigEndian.Uint16(header[2:]) != 0 {
					t.Fatal("profile negotiation failed", err)
				}
				m.mu.Lock()
				caps := m.capabilities
				m.mu.Unlock()
				if caps != (sessionauth.Capabilities{WireVersion: 4, Attachments: true, Control: true}) {
					t.Fatal("0.0.11 obtained withdrawal capability")
				}
				if err := engine.Queue(o.ID, false); err != nil {
					t.Fatal(err)
				}
				var kind [1]byte
				if _, err := io.ReadFull(peer, kind[:]); err != nil || kind[0] != transferPacket {
					t.Fatal("missing legacy offer", err)
				}
				offer, err := legacytransfer.ReadFrame(peer)
				if err != nil || offer.Kind != legacytransfer.OfferFrame {
					t.Fatal("legacy parser rejected offer", err)
				}
				if _, err := peer.Write([]byte{transferPacket}); err != nil {
					t.Fatal(err)
				}
				if err := legacytransfer.WriteFrame(peer, legacytransfer.Frame{Kind: legacytransfer.AcceptFrame, ID: o.ID, SHA256: o.SHA256, Credit: [16]byte{1}}); err != nil {
					t.Fatal(err)
				}
				select {
				case <-gated.started:
				case <-time.After(time.Second):
					t.Fatal("legacy transfer was not accepted")
				}
				if clear {
					err = engine.ClearHistory(func() error { return m.conversation.ClearHistory() })
				} else {
					err = engine.Withdraw(o.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				if record, err := source.Get(o.ID); err != nil || record.State != transfer.Declined || !record.PendingWithdrawal {
					t.Fatal("legacy cancellation was not retained", err)
				}
				if _, err := m.Send(id, "synthetic text after legacy cancellation"); err != nil {
					t.Fatal(err)
				}
				for {
					if _, err := io.ReadFull(peer, kind[:]); err != nil {
						t.Fatal("legacy connection dropped after cancellation", err)
					}
					if kind[0] == transferPacket {
						frame, err := legacytransfer.ReadFrame(peer)
						if err != nil || frame.Kind == legacytransfer.DeclineFrame {
							t.Fatal("unsupported withdrawal reached 0.0.11 receiver", err)
						}
						continue
					}
					if kind[0] != dataPacket {
						t.Fatal("unexpected legacy packet")
					}
					text, err := chat.ReadFrame(peer)
					if err != nil || text.Type != chat.MessageFrame {
						t.Fatal("text framing failed", err)
					}
					if _, err := peer.Write([]byte{dataPacket}); err != nil {
						t.Fatal(err)
					}
					if err := chat.WriteFrame(peer, chat.Frame{Version: 1, Type: chat.AckFrame, ID: text.ID}); err != nil {
						t.Fatal(err)
					}
					break
				}
				until := time.Now().Add(time.Second)
				for len(m.conversation.Outbox()) != 0 {
					if time.Now().After(until) {
						t.Fatal("legacy text ACK failed")
					}
					time.Sleep(time.Millisecond)
				}
			})
		}
	}
}
