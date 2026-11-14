package chat

import (
	"errors"
	"testing"

	"whatsup/internal/message"
	"whatsup/internal/pairing"
)

func TestReceiveCommittedDistinguishesNewDuplicateAndUncertainWrites(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	c := openTest(t, store, contact, Options{})
	frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: recordID(1), Text: "synthetic incoming notification"}
	ack, fresh, err := c.ReceiveCommitted(contact.Peer(), frame)
	if err != nil || !fresh || ack.ID != frame.ID || ack.Type != AckFrame {
		t.Fatal("new durable receive did not identify its commit")
	}
	c = openTest(t, store, contact, Options{})
	if ack, fresh, err := c.ReceiveCommitted(contact.Peer(), frame); err != nil || fresh || ack.ID != frame.ID {
		t.Fatal("retry produced another new-message indication")
	}
	frame.Text = "different synthetic content"
	if ack, fresh, err := c.ReceiveCommitted(contact.Peer(), frame); !errors.Is(err, ErrConflict) || fresh || ack.ID != "" {
		t.Fatal("conflicting retry produced new-message authority")
	}
	for _, after := range []bool{false, true} {
		s := &memoryStore{failOn: "receive", failAfter: after}
		other := openTest(t, s, testContact(t, 3, 4), Options{})
		if ack, fresh, err := other.ReceiveCommitted(testKey(t, 4), frame); err == nil || fresh || ack.ID != "" {
			t.Fatal("uncertain persistence produced ACK or notification")
		}
	}
}

func TestIncomingPreviewRejectsOutgoingHiddenMissingAndRevoked(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	c := openTest(t, store, contact, Options{})
	frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: recordID(0), Text: "synthetic visible notification"}
	if _, _, err := c.ReceiveCommitted(contact.Peer(), frame); err != nil {
		t.Fatal(err)
	}
	if item, err := c.GetVisibleIncoming(frame.ID); err != nil || item.Text != frame.Text {
		t.Fatal("exact incoming preview unavailable")
	}
	outgoing, err := c.Queue("synthetic private outgoing")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetVisibleIncoming(outgoing.ID); !errors.Is(err, message.ErrNotFound) {
		t.Fatal("outgoing text became notification preview")
	}
	if _, err := c.GetVisibleIncoming(recordID(99)); !errors.Is(err, message.ErrNotFound) {
		t.Fatal("missing message became preview")
	}
	if err := c.DeleteLocal(frame.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetVisibleIncoming(frame.ID); !errors.Is(err, message.ErrNotFound) {
		t.Fatal("deleted incoming text became preview")
	}
	contact.Revoke()
	if _, err := c.GetVisibleIncoming(outgoing.ID); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked contact read preview")
	}
}
