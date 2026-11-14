package chat

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"whatsup/internal/identity"
	"whatsup/internal/message"
	"whatsup/internal/pairing"
)

func testKey(t *testing.T, fill byte) identity.PublicKey {
	t.Helper()
	i, err := identity.FromSeed(bytes.Repeat([]byte{fill}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return i.PublicKey()
}

func testContact(t *testing.T, local, peer byte) *pairing.Contact {
	t.Helper()
	manager, err := pairing.NewManager(testKey(t, local), pairing.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	endpoint := tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()}, ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()}, PresharedKey: tailcat.NewPresharedKey(), RegionID: 1}
	invite, err := manager.Create(string(endpoint.Addr()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Reserve(invite.Link(), testKey(t, peer)); err != nil {
		t.Fatal(err)
	}
	contact, err := manager.Approve(testKey(t, peer).Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(contact.Revoke)
	return contact
}

func openTest(t *testing.T, store Store, contact *pairing.Contact, options Options) *Conversation {
	t.Helper()
	c, err := Open(store, contact, options)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func recordID(value uint64) string {
	var id [16]byte
	binary.BigEndian.PutUint64(id[8:], value)
	return base64.RawURLEncoding.EncodeToString(id[:])
}

func TestTwoIndependentDomainsDurableDedupAndAcknowledgement(t *testing.T) {
	// Synthetic approved-peer inputs isolate transactional domain rules. These
	// are not two-machine, encryption or network-path verification.
	storeA, storeB := &memoryStore{}, &memoryStore{}
	contactA, contactB := testContact(t, 1, 2), testContact(t, 2, 1)
	a := openTest(t, storeA, contactA, Options{})
	clock := time.Now()
	b := openTest(t, storeB, contactB, Options{Now: func() time.Time { return clock }})
	queued, err := a.Queue("synthetic durable message")
	if err != nil || len(a.Outbox()) != 1 {
		t.Fatal("queue was not committed")
	}
	a = openTest(t, storeA, contactA, Options{})
	frame, due, err := a.NextAttempt(contactA.Peer())
	if err != nil || !due || frame.ID != queued.ID || len(a.Outbox()) != 1 {
		t.Fatal("reopen or write lost stable outbox membership")
	}
	ack, err := b.Receive(contactB.Peer(), frame)
	if err != nil || len(b.Messages()) != 1 {
		t.Fatal("saved receive did not acknowledge")
	}
	// Lose ACK and restart the receiver. Locally stamped receive time changes,
	// while the original stable ID/body still deduplicates without another write.
	clock = clock.Add(time.Hour)
	b = openTest(t, storeB, contactB, Options{Now: func() time.Time { return clock }})
	writes := storeB.writes
	ack, err = b.Receive(contactB.Peer(), frame)
	if err != nil || storeB.writes != writes || len(b.Messages()) != 1 {
		t.Fatal("lost ACK reinserted a durable record")
	}
	if err = a.Acknowledge(contactA.Peer(), ack); err != nil {
		t.Fatal(err)
	}
	a = openTest(t, storeA, contactA, Options{})
	if len(a.Outbox()) != 0 || !a.Messages()[0].Delivered {
		t.Fatal("ACK did not atomically update delivery/outbox")
	}
	if err = a.Acknowledge(contactA.Peer(), ack); err != nil {
		t.Fatal("duplicate ACK was not idempotent")
	}
	frame.Text = "different body for same ID"
	if _, err = b.Receive(contactB.Peer(), frame); !errors.Is(err, ErrConflict) {
		t.Fatal("stable message ID changed content")
	}
}

func TestThirdPeerAndRevokedContactCannotReadOrMutateState(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	c := openTest(t, store, contact, Options{})
	queued, _ := c.Queue("synthetic outgoing")
	frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: recordID(1), Text: "synthetic incoming"}
	writes, reads := store.writes, store.reads
	if _, _, err := c.NextAttempt(testKey(t, 3)); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("third peer sent")
	}
	if _, err := c.Receive(testKey(t, 3), frame); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("third peer received")
	}
	if err := c.Acknowledge(testKey(t, 3), Frame{Version: ProtocolVersion, Type: AckFrame, ID: queued.ID}); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("third peer ACKed")
	}
	if err := c.ResetRetries(testKey(t, 3)); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("third peer rescheduled")
	}
	if store.writes != writes || store.reads != reads {
		t.Fatal("third peer touched the repository")
	}
	contact.Revoke()
	if _, err := c.Queue("after revoke"); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked contact queued")
	}
	if _, err := c.Page(0, 10); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked contact read history")
	}
	if err := c.DeleteLocal(queued.ID); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked contact deleted")
	}
	if err := c.ClearHistory(); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked contact cleared history")
	}
	if _, err := c.Usage(); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked contact obtained storage information")
	}
	if len(c.Messages()) != 0 || len(c.Outbox()) != 0 {
		t.Fatal("revoked handle exposed stale snapshots")
	}
	if _, err := Open(store, contact, Options{}); !errors.Is(err, ErrContact) {
		t.Fatal("revoked contact reopened")
	}
	if _, err := Open(store, nil, Options{}); !errors.Is(err, ErrContact) {
		t.Fatal("pending contact opened")
	}
}

func TestRetryBackoffIsMemoryOnlyAndRebuildsReadyAfterReopen(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	clock := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	var spans []time.Duration
	options := Options{Now: func() time.Time { return clock }, Jitter: func(minimum, maximum time.Duration) (time.Duration, error) {
		if minimum < RetryMin || maximum > RetryMax || minimum > maximum {
			t.Fatal("unbounded retry range")
		}
		spans = append(spans, maximum-minimum)
		return maximum, nil
	}}
	c := openTest(t, store, contact, options)
	queued, _ := c.Queue("synthetic retry")
	writes := store.writes
	for attempt := uint32(1); attempt <= 16; attempt++ {
		frame, due, err := c.NextAttempt(contact.Peer())
		if err != nil || !due || frame.ID != queued.ID || store.writes != writes {
			t.Fatal("retry persisted scheduling or failed to send due record")
		}
		pending := c.Outbox()[0]
		if pending.Count != attempt || !pending.NextAttempt.After(clock) || pending.NextAttempt.Sub(clock) > RetryMax {
			t.Fatal("unbounded retry schedule")
		}
		if _, due, err := c.NextAttempt(contact.Peer()); err != nil || due {
			t.Fatal("retry ignored its in-memory deadline")
		}
		clock = pending.NextAttempt
	}
	if spans[0] == 0 || spans[len(spans)-1] == 0 {
		t.Fatal("retry delay has no jitter range")
	}
	c.jitter = func(_, _ time.Duration) (time.Duration, error) { return 0, nil }
	if _, _, err := c.NextAttempt(contact.Peer()); !errors.Is(err, ErrRetry) || store.writes != writes {
		t.Fatal("invalid jitter changed storage or scheduled output")
	}
	c = openTest(t, store, contact, options)
	if c.Outbox()[0].Count != 0 || c.Outbox()[0].NextAttempt.After(clock) {
		t.Fatal("reopen restored stale retry timestamps")
	}
	if frame, due, err := c.NextAttempt(contact.Peer()); err != nil || !due || frame.ID != queued.ID || store.writes != writes {
		t.Fatal("unlock did not rebuild ready encrypted outbox")
	}
	store.failOn = "queue"
	if err := c.ResetRetries(contact.Peer()); err != nil || store.writes != writes {
		t.Fatal("session retry reset attempted persistence")
	}
	if _, due, err := c.NextAttempt(contact.Peer()); err != nil || !due {
		t.Fatal("fresh session retained old backoff")
	}
}

func TestRepositoryFailuresNeverReturnUnsavedSuccessAndRequireReopen(t *testing.T) {
	for _, operation := range []string{"queue", "receive", "ack", "get", "page", "usage"} {
		t.Run(operation, func(t *testing.T) {
			store := &memoryStore{}
			contact := testContact(t, 1, 2)
			c := openTest(t, store, contact, Options{})
			queued, _ := c.Queue("synthetic previously saved text")
			store.failOn = operation
			var err error
			switch operation {
			case "queue":
				var item Message
				item, err = c.Queue("must not report queued")
				if item.ID != "" {
					t.Fatal("failed queue returned usable ID")
				}
			case "receive":
				var ack Frame
				ack, err = c.Receive(contact.Peer(), Frame{Version: ProtocolVersion, Type: MessageFrame, ID: recordID(1), Text: "must not ACK"})
				if ack.ID != "" {
					t.Fatal("failed receive returned ACK")
				}
			case "ack":
				err = c.Acknowledge(contact.Peer(), Frame{Version: ProtocolVersion, Type: AckFrame, ID: queued.ID})
			case "get":
				var frame Frame
				var due bool
				frame, due, err = c.NextAttempt(contact.Peer())
				if due || frame.ID != "" {
					t.Fatal("failed record authentication handed frame to sender")
				}
			case "page":
				_, err = c.Page(0, 10)
			case "usage":
				_, err = c.Usage()
			}
			if !errors.Is(err, ErrStore) {
				t.Fatalf("repository failure hidden: %v", err)
			}
			store.failOn = ""
			if _, err := c.Queue("cannot continue uncertain handle"); !errors.Is(err, ErrStore) {
				t.Fatal("uncertain repository resumed without reopening")
			}
			reopened := openTest(t, store, contact, Options{})
			if len(reopened.Outbox()) != 1 || len(reopened.Messages()) != 1 || reopened.Messages()[0].Delivered {
				t.Fatal("reopen lost previously committed state")
			}
		})
	}
}

func TestUncertainAcknowledgementReconcilesCommittedRecordsOnReopen(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	c := openTest(t, store, contact, Options{})
	queued, _ := c.Queue("synthetic uncertain transaction")
	store.failOn, store.failAfter = "ack", true
	if err := c.Acknowledge(contact.Peer(), Frame{Version: ProtocolVersion, Type: AckFrame, ID: queued.ID}); !errors.Is(err, ErrStore) {
		t.Fatal("uncertain commit reported success")
	}
	if len(c.pending) != 1 {
		t.Fatal("failed ACK removed in-memory retry before confirmed commit")
	}
	if _, _, err := c.NextAttempt(contact.Peer()); !errors.Is(err, ErrStore) {
		t.Fatal("uncertain handle kept transmitting")
	}
	store.failOn = ""
	c = openTest(t, store, contact, Options{})
	if len(c.Outbox()) != 0 || !c.Messages()[0].Delivered {
		t.Fatal("reopen failed to reconcile committed ACK")
	}
}

func TestHistoryHasNoLifetimeCapAndDuplicateSurvivesPaging(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	c := openTest(t, store, contact, Options{})
	for index := uint64(1); index <= 10001; index++ {
		if _, err := c.Receive(contact.Peer(), Frame{Version: ProtocolVersion, Type: MessageFrame, ID: recordID(index), Text: "synthetic retained text"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.rows) != 10001 || !c.HasCapacity() {
		t.Fatal("old lifetime message cap survived")
	}
	latest, err := c.Page(0, 100)
	if err != nil || len(latest) != 100 || latest[0].Sequence != 9902 || latest[99].Sequence != 10001 {
		t.Fatal("latest page is not bounded and chronological")
	}
	older, err := c.Page(latest[0].Sequence, 100)
	if err != nil || len(older) != 100 || older[len(older)-1].Sequence != 9901 {
		t.Fatal("exclusive page cursor skipped or repeated a row")
	}
	if len(c.Messages()) != 100 {
		t.Fatal("compatibility snapshot leaked full history")
	}
	writes := store.writes
	if _, err := c.Receive(contact.Peer(), Frame{Version: ProtocolVersion, Type: MessageFrame, ID: recordID(1), Text: "synthetic retained text"}); err != nil || store.writes != writes {
		t.Fatal("old paged-out message lost deduplication evidence")
	}
	encoded, _ := json.Marshal(c.Messages())
	if bytes.Contains(encoded, []byte("sequence")) || bytes.Contains(encoded, []byte("Sequence")) {
		t.Fatal("internal sequence leaked into legacy message DTO")
	}
}

func TestPageHasAggregateByteBoundAndUnbrokenCursor(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	c := openTest(t, store, contact, Options{})
	for range 100 {
		if _, err := c.Queue(strings.Repeat("x", MaxTextBytes)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := c.Page(0, 100)
	if err != nil || len(page) != 64 {
		t.Fatal("page exceeded aggregate text budget")
	}
	total := 0
	for _, item := range page {
		total += len(item.Text)
	}
	if total != MaxPageTextBytes {
		t.Fatal("wrong aggregate page text budget")
	}
	older, err := c.Page(page[0].Sequence, 100)
	if err != nil || len(older) != 36 || older[len(older)-1].Sequence+1 != page[0].Sequence {
		t.Fatal("trimming a page skipped older history")
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, err := c.Page(0, limit); !errors.Is(err, ErrPage) {
			t.Fatal("unbounded page admitted")
		}
	}
}

func TestQuotaFailureIsVisibleAndDuplicatesStillAcknowledge(t *testing.T) {
	store := &memoryStore{quota: 133}
	contact := testContact(t, 1, 2)
	c := openTest(t, store, contact, Options{})
	frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: recordID(1), Text: "first"}
	if _, err := c.Receive(contact.Peer(), frame); err != nil {
		t.Fatal(err)
	}
	if c.HasCapacity() {
		t.Fatal("disk quota not reflected")
	}
	if _, err := c.Receive(contact.Peer(), frame); err != nil {
		t.Fatal("saved duplicate was rejected at quota")
	}
	frame.ID = recordID(2)
	if ack, err := c.Receive(contact.Peer(), frame); !errors.Is(err, ErrCapacity) || ack.ID != "" {
		t.Fatal("full storage ACKed unsaved message")
	}
	if item, err := c.Queue("new"); !errors.Is(err, ErrCapacity) || item.ID != "" {
		t.Fatal("full storage accepted outgoing text")
	}
	store.quota = 4096
	if _, err := c.Queue("space available again"); err != nil {
		t.Fatal("quota error poisoned otherwise valid repository")
	}
}

func TestLocalDeleteAndClearKeepPendingDeliveryAndDeduplication(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	c := openTest(t, store, contact, Options{})
	queued, _ := c.Queue("synthetic pending text")
	frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: recordID(1), Text: "synthetic received"}
	if _, err := c.Receive(contact.Peer(), frame); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteLocal(queued.ID); err != nil {
		t.Fatal(err)
	}
	if len(c.Messages()) != 1 || len(c.Outbox()) != 1 {
		t.Fatal("local delete removed pending delivery or unrelated history")
	}
	if err := c.ClearHistory(); err != nil || len(c.Messages()) != 0 || len(c.Outbox()) != 1 {
		t.Fatal("clear changed contact authority or removed outbox")
	}
	c = openTest(t, store, contact, Options{})
	out, due, err := c.NextAttempt(contact.Peer())
	if err != nil || !due || out.ID != queued.ID || out.Text != "synthetic pending text" {
		t.Fatal("hidden pending body could not resume after restart")
	}
	writes := store.writes
	if _, err := c.Receive(contact.Peer(), frame); err != nil || store.writes != writes || len(c.Messages()) != 0 {
		t.Fatal("clear allowed a lost ACK retry to redisplay old text")
	}
	ack := Frame{Version: ProtocolVersion, Type: AckFrame, ID: queued.ID}
	if err := c.Acknowledge(contact.Peer(), ack); err != nil || len(c.Outbox()) != 0 {
		t.Fatal("hidden pending message failed ACK")
	}
	if err := c.Acknowledge(contact.Peer(), ack); err != nil {
		t.Fatal("repeated ACK for locally hidden delivered record failed")
	}
	if _, err := c.Queue("same contact remains approved"); err != nil {
		t.Fatal("clear revoked the contact")
	}
}

func TestMalformedPersistedRecordsFailClosed(t *testing.T) {
	for _, change := range []string{"id", "body", "direction", "delivered", "sequence"} {
		t.Run(change, func(t *testing.T) {
			store := &memoryStore{}
			contact := testContact(t, 1, 2)
			c := openTest(t, store, contact, Options{})
			queued, _ := c.Queue("synthetic persisted body")
			item := store.rows[queued.ID]
			switch change {
			case "id":
				item.ID = recordID(44)
			case "body":
				item.Text = "spoof\u202e"
			case "direction":
				item.Direction = "unknown"
			case "delivered":
				item.Delivered = true
			case "sequence":
				item.Sequence = 0
			}
			store.rows[queued.ID] = item
			if frame, due, err := c.NextAttempt(contact.Peer()); !errors.Is(err, ErrCorrupt) || due || frame.ID != "" {
				t.Fatal("malformed outbox record entered a network frame")
			}
			if _, err := c.Queue("must stay failed"); !errors.Is(err, ErrStore) {
				t.Fatal("corrupt repository continued")
			}
		})
	}
	for _, ids := range [][]string{{"../not-an-id"}, {recordID(1), recordID(1)}} {
		if _, err := Open(&memoryStore{pendingOverride: ids}, testContact(t, 1, 2), Options{}); !errors.Is(err, ErrCorrupt) {
			t.Fatal("invalid pending index admitted on open")
		}
	}
	if _, err := Open(&memoryStore{failOn: "pending"}, testContact(t, 1, 2), Options{}); !errors.Is(err, ErrStore) {
		t.Fatal("failed outbox recovery admitted a conversation")
	}
	var zero *Conversation
	if _, err := zero.Queue("zero"); !errors.Is(err, ErrContact) {
		t.Fatal("zero conversation queued")
	}
}

type blockingStore struct {
	*memoryStore
	entered, release chan struct{}
	once             sync.Once
}

func (s *blockingStore) Queue(item Message) error {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.memoryStore.Queue(item)
}

func TestRevocationWaitsForAdmittedDurableOperation(t *testing.T) {
	store := &blockingStore{memoryStore: &memoryStore{}, entered: make(chan struct{}), release: make(chan struct{})}
	contact := testContact(t, 1, 2)
	c := openTest(t, store, contact, Options{})
	release := sync.OnceFunc(func() { close(store.release) })
	defer release()
	queued := make(chan error, 1)
	go func() { _, err := c.Queue("synthetic admitted write"); queued <- err }()
	<-store.entered
	revoked := make(chan struct{})
	go func() { contact.Revoke(); close(revoked) }()
	select {
	case <-revoked:
		t.Fatal("revocation passed an admitted durable operation")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("contact/repository lock order deadlocked")
	}
	if _, err := c.Queue("after revoke"); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("operation admitted after revocation")
	}
}

var _ message.Repository = (*memoryStore)(nil)
