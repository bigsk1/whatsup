package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"whatsup/internal/chat"
)

func TestIncomingCallbackFollowsNewDurableCommitAndNeverRetry(t *testing.T) {
	m, server, client, conversation, _ := exchangeFixture(t)
	m.record.ID = "AQEBAQEBAQEBAQEBAQEBAQ"
	m.sessionActive = true
	var notices atomic.Int32
	m.onIncoming = func(incoming Incoming) {
		// Reentering a guarded domain lookup proves receive released its lock.
		stored, err := conversation.GetVisibleIncoming(incoming.MessageID)
		if err != nil || stored.Text != "synthetic committed notification" || incoming.ContactID != m.record.ID {
			t.Error("notification preceded durable exact-ID receive")
		}
		notices.Add(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.exchange(ctx, server, conversation) }()
	consumeInitialProfile(t, client)
	frame := chat.Frame{Version: chat.ProtocolVersion, Type: chat.MessageFrame, ID: capacityID(123), Text: "synthetic committed notification"}
	for range 2 {
		if err := writePacket(client, packet{kind: dataPacket, frame: frame}); err != nil {
			t.Fatal(err)
		}
		ack, err := readPacket(client)
		if err != nil || ack.frame.ID != frame.ID || ack.frame.Type != chat.AckFrame {
			t.Fatal("durable retry did not acknowledge")
		}
	}
	if notices.Load() != 1 {
		t.Fatal("duplicate retry produced multiple notifications")
	}
	cancel()
	waitExchange(t, result)
	for _, value := range []any{Incoming{ContactID: "secret", MessageID: "secret"}, Notification{Name: "secret", Text: "secret"}} {
		if fmt.Sprintf("%+v", value) == "secret" {
			t.Fatal("notification formatting exposed content")
		}
		if wire, err := json.Marshal(value); err == nil || len(wire) != 0 {
			t.Fatal("backend notification was serializable")
		}
	}
}

func TestRegistryNotificationExactLookupRejectsDeletedOutgoingAndRetired(t *testing.T) {
	a, b := newRegistryPeer(t), newRegistryPeer(t)
	id := seedRegistryAssociation(t, a, b)
	n := &registryNetwork{}
	openRegistry(t, a, n, nil)
	openRegistry(t, b, n, nil)
	if _, err := a.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.registry.Connect(id); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, a, id, func(s Snapshot) bool { return s.State == "connected" })
	waitRegistry(t, b, id, func(s Snapshot) bool { return s.State == "connected" })
	if err := a.app.SetContactName(id, "Synthetic local label"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.registry.Send(id, "synthetic private preview"); err != nil {
		t.Fatal(err)
	}
	got := waitRegistry(t, a, id, func(s Snapshot) bool { return len(s.Messages) == 1 })
	manager, err := a.registry.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	incoming := Incoming{ContactID: id, MessageID: got.Messages[0].ID, origin: manager.managerState}
	if _, err := a.registry.Notification(Incoming{ContactID: id, MessageID: incoming.MessageID}); err == nil {
		t.Fatal("forged notice lacked runtime origin")
	}
	if preview, err := a.registry.Notification(incoming); err != nil || preview.Name != "Synthetic local label" || preview.Text != "synthetic private preview" {
		t.Fatal("notification lookup missed exact locally approved incoming record")
	}
	// A queued notice can survive the owning native worker's lock boundary.
	// The same saved association in a replacement runtime is not its origin.
	a.registry.Close()
	openRegistry(t, a, n, nil)
	if _, err := a.registry.Notification(incoming); err == nil {
		t.Fatal("retired runtime notice was replayed after unlock")
	}
	manager, err = a.registry.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	incoming.origin = manager.managerState // Synthetic origin stays within this package's test.
	outgoing, err := a.registry.Send(id, "synthetic outgoing excluded")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.registry.Notification(Incoming{ContactID: id, MessageID: outgoing.Messages[len(outgoing.Messages)-1].ID, origin: manager.managerState}); err == nil {
		t.Fatal("outgoing record became preview")
	}
	if _, err := a.registry.DeleteMessage(id, incoming.MessageID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.registry.Notification(incoming); err == nil {
		t.Fatal("deleted record became preview")
	}
	records, _ := a.app.CommittedContacts()
	if err := a.app.ForgetCommittedContact(records[0], func() error { return a.registry.Retire(id) }); err != nil {
		t.Fatal(err)
	}
	if _, err := a.registry.Notification(incoming); err == nil {
		t.Fatal("retired contact became preview")
	}
}
