package chat

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zalando/go-keyring"
	"tailscale.com/types/key"

	"whatsup/internal/identity"
	"whatsup/internal/pairing"
	"whatsup/internal/storage"
)

type memoryStore struct {
	data             []byte
	fail             bool
	writeBeforeError bool
	saves            int
}

func (store *memoryStore) Load() ([]byte, error) {
	if store.data == nil {
		return nil, os.ErrNotExist
	}
	return bytes.Clone(store.data), nil
}

func (store *memoryStore) Save(data []byte) error {
	store.saves++
	if !store.fail || store.writeBeforeError {
		store.data = bytes.Clone(data)
	}
	if store.fail {
		return errors.New("synthetic persistence failure")
	}
	return nil
}

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
	endpoint := tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()},
		PresharedKey:      tailcat.NewPresharedKey(), RegionID: 1}
	invite, err := manager.Create(string(endpoint.Addr()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reserve(invite.Link(), testKey(t, peer)); err != nil {
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
	conversation, err := Open(store, contact, options)
	if err != nil {
		t.Fatal(err)
	}
	return conversation
}

func TestTwoIndependentDomainsDurableDedupAndAcknowledgement(t *testing.T) {
	// These domains have synthetic authenticated-peer inputs. This is a durable
	// protocol test, not a real network, mutual-approval, direct, or relay test.
	storeA, storeB := &memoryStore{}, &memoryStore{}
	contactA, contactB := testContact(t, 1, 2), testContact(t, 2, 1)
	a := openTest(t, storeA, contactA, Options{})
	b := openTest(t, storeB, contactB, Options{})
	message, err := a.Queue("synthetic durable message")
	if err != nil || len(a.Outbox()) != 1 {
		t.Fatal("message not durably queued")
	}
	a = openTest(t, storeA, contactA, Options{})
	frame, due, err := a.NextAttempt(contactA.Peer())
	if err != nil || !due || frame.ID != message.ID || len(a.Outbox()) != 1 {
		t.Fatal("reopen changed stable ID or send deleted queue entry")
	}
	ack, err := b.Receive(contactB.Peer(), frame)
	if err != nil || ack.ID != frame.ID || len(b.Messages()) != 1 {
		t.Fatal("receive did not durably acknowledge")
	}
	// Lose the first acknowledgement, restart the receiver, then retry the exact
	// ID. No second message or second persistence operation is needed.
	b = openTest(t, storeB, contactB, Options{})
	saves := storeB.saves
	ack, err = b.Receive(contactB.Peer(), frame)
	if err != nil || len(b.Messages()) != 1 || storeB.saves != saves {
		t.Fatal("restart lost durable deduplication")
	}
	if err := a.Acknowledge(contactA.Peer(), ack); err != nil {
		t.Fatal(err)
	}
	a = openTest(t, storeA, contactA, Options{})
	if len(a.Outbox()) != 0 || len(a.Messages()) != 1 || !a.Messages()[0].Delivered {
		t.Fatal("acknowledged queue/history state did not survive reopen")
	}
	if err := a.Acknowledge(contactA.Peer(), ack); err != nil {
		t.Fatal("repeated authenticated ack was not idempotent")
	}
	frame.Text = "substituted content"
	if _, err := b.Receive(contactB.Peer(), frame); !errors.Is(err, ErrConflict) {
		t.Fatal("stable message ID was allowed to change content")
	}
}

func TestThirdPeerAndRevokedContactCannotMutateState(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	conversation := openTest(t, store, contact, Options{})
	queued, _ := conversation.Queue("synthetic outgoing")
	ack := Frame{Version: ProtocolVersion, Type: AckFrame, ID: queued.ID}
	incoming := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: "synthetic incoming"}
	before := bytes.Clone(store.data)
	if _, _, err := conversation.NextAttempt(testKey(t, 3)); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("third peer could send")
	}
	if _, err := conversation.Receive(testKey(t, 3), incoming); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("third peer could insert history")
	}
	if err := conversation.Acknowledge(testKey(t, 3), ack); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("third peer removed queued message")
	}
	if !bytes.Equal(before, store.data) {
		t.Fatal("third-peer access mutated durable state")
	}
	contact.Revoke()
	if _, err := conversation.Queue("after revocation"); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked contact queued new message")
	}
	if _, _, err := conversation.NextAttempt(contact.Peer()); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked session could send")
	}
	if _, err := conversation.Receive(contact.Peer(), incoming); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked session received message")
	}
	if err := conversation.Acknowledge(contact.Peer(), ack); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("revoked session acknowledged message")
	}
	if _, err := Open(store, contact, Options{}); !errors.Is(err, ErrContact) {
		t.Fatal("revoked contact reopened conversation")
	}
	if _, err := Open(store, nil, Options{}); !errors.Is(err, ErrContact) {
		t.Fatal("pending/no contact opened conversation")
	}
}

func TestSaveFailuresReturnNoSuccessAndKeepMemoryUnchanged(t *testing.T) {
	for _, operation := range []string{"queue", "send", "receive", "ack"} {
		t.Run(operation, func(t *testing.T) {
			store := &memoryStore{}
			contact := testContact(t, 1, 2)
			conversation := openTest(t, store, contact, Options{})
			queued, _ := conversation.Queue("synthetic existing")
			before, _ := json.Marshal(conversation.state)
			store.fail = true
			var err error
			switch operation {
			case "queue":
				message, failure := conversation.Queue("must not report queued")
				err = failure
				if message.ID != "" {
					t.Fatal("failed queue returned a usable ID")
				}
			case "send":
				frame, due, failure := conversation.NextAttempt(contact.Peer())
				err = failure
				if due || frame.ID != "" {
					t.Fatal("failed retry save handed frame to sender")
				}
			case "receive":
				ack, failure := conversation.Receive(contact.Peer(), Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: "must not acknowledge"})
				err = failure
				if ack.ID != "" {
					t.Fatal("unsaved receive returned acknowledgement")
				}
			case "ack":
				err = conversation.Acknowledge(contact.Peer(), Frame{Version: ProtocolVersion, Type: AckFrame, ID: queued.ID})
			}
			if !errors.Is(err, ErrStore) {
				t.Fatalf("save failure not returned: %v", err)
			}
			after, _ := json.Marshal(conversation.state)
			if !bytes.Equal(before, after) {
				t.Fatal("save failure changed in-memory queue/history")
			}
			store.fail = false
			if _, err := conversation.Queue("cannot continue uncertain writer"); !errors.Is(err, ErrStore) {
				t.Fatal("writer resumed without reopening uncertain state")
			}
			reopened := openTest(t, store, contact, Options{})
			if len(reopened.Outbox()) != 1 || len(reopened.Messages()) != 1 {
				t.Fatal("reopen lost previously saved queued message")
			}
		})
	}
}

func TestPostRenameSyncFailureRequiresReopenToReconcile(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	conversation := openTest(t, store, contact, Options{})
	message, _ := conversation.Queue("synthetic uncertain ack")
	store.fail, store.writeBeforeError = true, true
	if err := conversation.Acknowledge(contact.Peer(), Frame{Version: ProtocolVersion, Type: AckFrame, ID: message.ID}); !errors.Is(err, ErrStore) {
		t.Fatal("post-write durability failure ignored")
	}
	if len(conversation.Outbox()) != 1 {
		t.Fatal("memory committed an uncertain save")
	}
	store.fail = false
	reopened := openTest(t, store, contact, Options{})
	if len(reopened.Outbox()) != 0 || !reopened.Messages()[0].Delivered {
		t.Fatal("reopen did not reconcile actual stored state")
	}
}

func TestRetriesPersistWithBoundsAndJitter(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	var elapsed atomic.Int64
	base := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	var spans []time.Duration
	options := Options{
		Now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) },
		Jitter: func(minimum, maximum time.Duration) (time.Duration, error) {
			if minimum < RetryMin || maximum > RetryMax || minimum > maximum {
				t.Fatal("unbounded retry jitter")
			}
			spans = append(spans, maximum-minimum)
			return maximum, nil
		},
	}
	conversation := openTest(t, store, contact, options)
	message, _ := conversation.Queue("synthetic retry")
	for attempt := uint32(1); attempt <= 16; attempt++ {
		frame, due, err := conversation.NextAttempt(contact.Peer())
		if err != nil || !due || frame.ID != message.ID {
			t.Fatal("due retry not returned")
		}
		pending := conversation.Outbox()[0]
		if pending.Count != attempt || !pending.NextAttempt.After(options.Now()) || pending.NextAttempt.Sub(options.Now()) > RetryMax {
			t.Fatal("retry count/deadline unbounded")
		}
		conversation = openTest(t, store, contact, options)
		if _, due, err := conversation.NextAttempt(contact.Peer()); due || err != nil {
			t.Fatal("restart retried before persisted deadline")
		}
		elapsed.Store(int64(pending.NextAttempt.Sub(base)))
	}
	if spans[0] == 0 || spans[len(spans)-1] == 0 {
		t.Fatal("retry schedule has no jitter range")
	}
	conversation.jitter = func(_, _ time.Duration) (time.Duration, error) { return 0, nil }
	before := store.saves
	if _, _, err := conversation.NextAttempt(contact.Peer()); !errors.Is(err, ErrRetry) || store.saves != before {
		t.Fatal("invalid jitter bypassed bounded backoff")
	}
}

func TestHistoryAndQueueAreBoundedWithoutEvictingDedup(t *testing.T) {
	contact := testContact(t, 1, 2)
	for _, byBytes := range []bool{false, true} {
		t.Run(fmt.Sprint(byBytes), func(t *testing.T) {
			store := &memoryStore{}
			conversation := openTest(t, store, contact, Options{})
			text, count := "x", MaxMessages
			if byBytes {
				text, count = strings.Repeat("<", MaxTextBytes), MaxStoredTextBytes/MaxTextBytes
			}
			for i := 0; i < count; i++ {
				if _, err := conversation.Queue(text); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := conversation.Queue("one too many"); !errors.Is(err, ErrCapacity) {
				t.Fatal("conversation grew past bounded capacity")
			}
			if len(store.data) > MaxSnapshotBytes {
				t.Fatal("JSON escaping grew beyond encrypted snapshot limit")
			}
			if _, err := conversation.Receive(contact.Peer(), Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: "full"}); !errors.Is(err, ErrCapacity) {
				t.Fatal("inbound message bypassed shared quota")
			}
			_ = openTest(t, store, contact, Options{})
		})
	}
}

func TestFreshAuthenticatedSessionResetsPersistedRetryWithoutAcknowledgement(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	clock := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	options := Options{Now: func() time.Time { return clock }, Jitter: func(_, maximum time.Duration) (time.Duration, error) { return maximum, nil }}
	conversation := openTest(t, store, contact, options)
	queued, _ := conversation.Queue("synthetic offline retry")
	for index := 0; index < 12; index++ {
		if _, due, err := conversation.NextAttempt(contact.Peer()); err != nil || !due {
			t.Fatal("failed to build persisted retry schedule")
		}
		if index < 11 {
			clock = conversation.Outbox()[0].NextAttempt
		}
	}
	if conversation.Outbox()[0].NextAttempt.Sub(clock) != RetryMax {
		t.Fatal("fixture did not reach maximum offline backoff")
	}
	before := bytes.Clone(store.data)
	if err := conversation.ResetRetries(testKey(t, 3)); err == nil || !bytes.Equal(before, store.data) {
		t.Fatal("unapproved peer reset the outbox schedule")
	}
	if err := conversation.ResetRetries(contact.Peer()); err != nil {
		t.Fatal(err)
	}
	conversation = openTest(t, store, contact, options)
	frame, due, err := conversation.NextAttempt(contact.Peer())
	if err != nil || !due || frame.ID != queued.ID || conversation.Messages()[0].Delivered || len(conversation.Outbox()) != 1 {
		t.Fatal("fresh authenticated session waited for old backoff or lost queued text")
	}
	if _, due, err := conversation.NextAttempt(contact.Peer()); err != nil || due {
		t.Fatal("session reset disabled bounded backoff within that session")
	}
	store.fail = true
	if err := conversation.ResetRetries(contact.Peer()); !errors.Is(err, ErrStore) {
		t.Fatal("retry reset hid persistence failure")
	}
}

func TestStoredIdentityAndStructureAreValidated(t *testing.T) {
	store := &memoryStore{}
	contact := testContact(t, 1, 2)
	conversation := openTest(t, store, contact, Options{})
	message, _ := conversation.Queue("synthetic bound snapshot")
	if _, err := Open(store, testContact(t, 1, 3), Options{}); !errors.Is(err, ErrCorrupt) {
		t.Fatal("different approved peer obtained old conversation")
	}
	for _, mutate := range []func(*snapshot){
		func(s *snapshot) { s.Version++ },
		func(s *snapshot) { s.Local = testKey(t, 4) },
		func(s *snapshot) { s.Messages = append(s.Messages, s.Messages[0]) },
		func(s *snapshot) { s.Messages[0].Delivered = true },
		func(s *snapshot) { s.Outbox = nil },
		func(s *snapshot) { s.Outbox[0].ID = syntheticID },
		func(s *snapshot) { s.Outbox[0].NextAttempt = time.Time{} },
	} {
		damaged := clone(conversation.state)
		mutate(&damaged)
		data, _ := json.Marshal(damaged)
		if _, err := Open(&memoryStore{data: data}, contact, Options{}); !errors.Is(err, ErrCorrupt) {
			t.Fatal("invalid durable state accepted")
		}
	}
	for _, data := range [][]byte{nil, []byte("{}"), []byte("null"), append([]byte(" "), store.data...), bytes.Repeat([]byte("x"), MaxSnapshotBytes+1)} {
		if data == nil {
			data = []byte{}
		}
		if _, err := Open(&memoryStore{data: data}, contact, Options{}); !errors.Is(err, ErrCorrupt) {
			t.Fatal("malformed snapshot accepted or silently replaced")
		}
	}
	if err := conversation.Acknowledge(contact.Peer(), Frame{Version: ProtocolVersion, Type: AckFrame, ID: syntheticID}); !errors.Is(err, ErrAck) {
		t.Fatal("unknown ack accepted")
	}
	incoming := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: message.ID, Text: "incoming collision"}
	if _, err := conversation.Receive(contact.Peer(), incoming); !errors.Is(err, ErrConflict) {
		t.Fatal("incoming collision overwrote outgoing message")
	}
	copyOfMessages := conversation.Messages()
	copyOfMessages[0].Text = "changed copy"
	if conversation.Messages()[0].Text != "synthetic bound snapshot" {
		t.Fatal("caller mutated internal history")
	}
}

type testKeys struct{ value string }

func (keys *testKeys) Get() (string, error) {
	if keys.value == "" {
		return "", keyring.ErrNotFound
	}
	return keys.value, nil
}

func (keys *testKeys) Set(value string) error { keys.value = value; return nil }

func TestConversationSnapshotRoundTripsThroughEncryptedVault(t *testing.T) {
	keys := &testKeys{}
	directory := filepath.Join(t.TempDir(), "private-conversation")
	vault, err := storage.Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	contact := testContact(t, 1, 2)
	conversation := openTest(t, vault, contact, Options{})
	text := "synthetic text must never appear in a plaintext chat file"
	message, err := conversation.Queue(text)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := os.ReadFile(filepath.Join(directory, "state.vault"))
	if err != nil || bytes.Contains(sealed, []byte(text)) || bytes.Contains(sealed, []byte(message.ID)) || bytes.Contains(sealed, []byte(base64.RawStdEncoding.EncodeToString([]byte(text)))) {
		t.Fatal("chat plaintext present in vault file")
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err = storage.Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	conversation = openTest(t, vault, contact, Options{})
	if len(conversation.Outbox()) != 1 || conversation.Messages()[0].ID != message.ID || conversation.Messages()[0].Text != text {
		t.Fatal("encrypted reopen lost pending message")
	}
	if err := conversation.Acknowledge(contact.Peer(), Frame{Version: ProtocolVersion, Type: AckFrame, ID: message.ID}); err != nil {
		t.Fatal(err)
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err = storage.Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	conversation = openTest(t, vault, contact, Options{})
	if len(conversation.Outbox()) != 0 || len(conversation.Messages()) != 1 || !conversation.Messages()[0].Delivered {
		t.Fatal("authenticated acknowledgement was not durable in encrypted vault")
	}
}

type blockingStore struct {
	base    memoryStore
	block   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (store *blockingStore) Load() ([]byte, error) { return store.base.Load() }
func (store *blockingStore) Save(data []byte) error {
	if store.block.Load() {
		close(store.entered)
		<-store.release
	}
	return store.base.Save(data)
}

func TestRevocationWaitsForAdmittedDurableOperation(t *testing.T) {
	store := &blockingStore{entered: make(chan struct{}), release: make(chan struct{})}
	contact := testContact(t, 1, 2)
	conversation := openTest(t, store, contact, Options{})
	store.block.Store(true)
	release := sync.OnceFunc(func() { close(store.release) })
	defer release()
	var wg sync.WaitGroup
	wg.Add(1)
	queued := make(chan error, 1)
	go func() {
		defer wg.Done()
		_, err := conversation.Queue("synthetic admitted operation")
		queued <- err
	}()
	<-store.entered
	revoked := make(chan struct{})
	go func() { contact.Revoke(); close(revoked) }()
	select {
	case <-revoked:
		t.Fatal("revocation returned while admitted persistence was still pending")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	wg.Wait()
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
	select {
	case <-revoked:
	case <-time.After(time.Second):
		t.Fatal("authorization lock order deadlocked revocation")
	}
	if _, err := conversation.Queue("after revoke returns"); !errors.Is(err, pairing.ErrPeer) {
		t.Fatal("operation admitted after revocation completed")
	}
}
