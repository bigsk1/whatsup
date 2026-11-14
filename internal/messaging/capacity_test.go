package messaging

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"whatsup/internal/chat"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

func capacityID(value uint64) string {
	var id [16]byte
	binary.BigEndian.PutUint64(id[8:], value)
	return base64.RawURLEncoding.EncodeToString(id[:])
}

func TestFullReceiverSendsAuthenticatedRefusalWithoutAcknowledgingOrEvicting(t *testing.T) {
	store := newMessageRepository(t)
	m, server, client, conversation, _ := exchangeFixture(t, store)
	if _, err := conversation.Receive(m.record.Peer, chat.Frame{Version: chat.ProtocolVersion, Type: chat.MessageFrame, ID: capacityID(1), Text: "synthetic retained history"}); err != nil {
		t.Fatal(err)
	}
	// Inject quota exhaustion, independently of lifetime history length. The
	// actual encrypted repository's quota/disk-full behavior has separate tests.
	store.full = true
	before := conversation.Messages()
	result := make(chan error, 1)
	go func() { result <- m.exchange(context.Background(), server, conversation) }()
	consumeInitialProfile(t, client)
	frame := chat.Frame{Version: chat.ProtocolVersion, Type: chat.MessageFrame, ID: capacityID(2), Text: "synthetic refused text"}
	if err := writePacket(client, packet{kind: dataPacket, frame: frame}); err != nil {
		t.Fatal(err)
	}
	refusal, err := readPacket(client)
	if err != nil || refusal.kind != capacityPacket || refusal.frame.ID != frame.ID {
		t.Fatal("receiver did not send a separate, ID-bound capacity refusal")
	}
	if err := <-result; !errors.Is(err, chat.ErrCapacity) {
		t.Fatal("full receiver did not terminate with capacity reason")
	}
	after := conversation.Messages()
	if len(after) != len(before) || len(conversation.Outbox()) != 0 {
		t.Fatal("capacity refusal inserted text or changed history/outbox")
	}
	for index := range before {
		if after[index] != before[index] {
			t.Fatal("full receiver evicted or changed a deduplication record")
		}
	}
	// A lost ACK for an already saved message must still be recoverable at quota.
	if _, err := conversation.Receive(m.record.Peer, chat.Frame{Version: chat.ProtocolVersion, Type: chat.MessageFrame, ID: before[0].ID, Text: before[0].Text}); err != nil {
		t.Fatal("full history rejected an existing saved message's retry")
	}
}

func TestCapacityRefusalIsSessionBoundAndNeverRemovesQueuedMessage(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "sent ID", false: "unsent ID"}[known], func(t *testing.T) {
			m, server, client, conversation, _ := exchangeFixture(t)
			queued, err := conversation.Queue("synthetic retained outbox")
			if err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { result <- m.exchange(context.Background(), server, conversation) }()
			consumeInitialProfile(t, client)
			outgoing, err := readPacket(client)
			if err != nil || outgoing.kind != dataPacket || outgoing.frame.ID != queued.ID {
				t.Fatal("queued text did not reach authenticated peer")
			}
			id := queued.ID
			if !known {
				id = capacityID(1234)
			}
			if err := writePacket(client, packet{kind: capacityPacket, frame: chat.Frame{Version: chat.ProtocolVersion, Type: chat.AckFrame, ID: id}}); err != nil {
				t.Fatal(err)
			}
			err = <-result
			if known && !errors.Is(err, errPeerCapacity) || !known && !errors.Is(err, errProtocol) {
				t.Fatal("capacity refusal accepted an unsent ID or lost its explicit reason")
			}
			if len(conversation.Outbox()) != 1 || conversation.Messages()[0].Delivered {
				t.Fatal("capacity refusal falsely acknowledged or removed queued text")
			}
		})
	}
}

func TestCapacityStopsManagerRetriesAndPreservesHistory(t *testing.T) {
	for _, failure := range []error{chat.ErrCapacity, errPeerCapacity} {
		t.Run(failure.Error(), func(t *testing.T) {
			m, store, profiles := newBlockedManager(t)
			store.armed = false
			if _, err := m.conversation.Queue("synthetic queued text preserved at capacity"); err != nil {
				t.Fatal(err)
			}
			before := m.record
			m.factories.Profile = func(context.Context, string) (*transport.RelayProfile, error) { profiles.Add(1); return nil, nil }
			m.factories.Listen = func(context.Context, storage.PendingRecord, *transport.RelayProfile) (Listener, error) {
				return nil, failure
			}
			if _, err := m.Connect(before.ID); err != nil {
				t.Fatal(err)
			}
			joined := make(chan struct{})
			go func() { m.workers.Wait(); close(joined) }()
			select {
			case <-joined:
			case <-time.After(time.Second):
				t.Fatal("capacity failure kept network retries running")
			}
			state := "capacity-reached"
			if errors.Is(failure, errPeerCapacity) {
				state = "peer-capacity-reached"
			}
			snapshot := m.Snapshot()
			if snapshot.State != state || snapshot.CanSend || !snapshot.CanConnect || len(snapshot.Messages) != 1 || snapshot.Messages[0].Delivered || profiles.Load() != 1 {
				t.Fatal("capacity failure lost history or remained silently usable")
			}
			if _, err := m.Send(before.ID, "not permitted"); err == nil {
				t.Fatal("stopped capacity state accepted new queued text")
			}
			if m.record != before {
				t.Fatal("capacity failure changed saved contact")
			}
		})
	}
}

func TestFullLocalStorageIsVisibleAndCannotQueueMore(t *testing.T) {
	m, store, _ := newBlockedManager(t)
	store.armed = false
	if _, err := m.conversation.Queue("synthetic retained text at disk quota"); err != nil {
		t.Fatal(err)
	}
	store.full = true
	snapshot := m.Snapshot()
	if snapshot.State != "capacity-reached" || snapshot.CanSend || !snapshot.CanConnect || len(snapshot.Messages) != 1 {
		t.Fatal("full local history was hidden or new queueing remained enabled")
	}
}

func TestCapacityPacketRejectsTextAndConsumesOnlyItsID(t *testing.T) {
	var wire bytes.Buffer
	wire.WriteByte(capacityPacket)
	if err := chat.WriteFrame(&wire, chat.Frame{Version: chat.ProtocolVersion, Type: chat.MessageFrame, ID: capacityID(1), Text: "not a capacity ID"}); err != nil {
		t.Fatal(err)
	}
	if _, err := readPacket(&wire); !errors.Is(err, errProtocol) {
		t.Fatal("capacity packet accepted a text message")
	}
	wire.WriteByte(capacityPacket)
	if err := chat.WriteFrame(&wire, chat.Frame{Version: chat.ProtocolVersion, Type: chat.AckFrame, ID: capacityID(1)}); err != nil {
		t.Fatal(err)
	}
	wire.WriteByte(pingPacket)
	wire.Write(make([]byte, 16))
	if p, err := readPacket(&wire); err != nil || p.kind != capacityPacket || p.frame.ID != capacityID(1) {
		t.Fatal("capacity packet could not be read")
	}
	if p, err := readPacket(&wire); err != nil || p.kind != pingPacket || wire.Len() != 0 {
		t.Fatal("capacity ID swallowed following heartbeat")
	}
}
