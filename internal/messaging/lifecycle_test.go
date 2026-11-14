package messaging

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"github.com/zalando/go-keyring"
	"tailscale.com/types/key"
	"whatsup/internal/chat"
	"whatsup/internal/identity"
	"whatsup/internal/pairing"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type lifecycleKeys struct{ value string }

func (k *lifecycleKeys) Get() (string, error) {
	if k.value == "" {
		return "", keyring.ErrNotFound
	}
	return k.value, nil
}
func (k *lifecycleKeys) Set(value string) error { k.value = value; return nil }

type blockedStore struct {
	data             []byte
	armed            bool
	entered, release chan struct{}
	once             sync.Once
}

func (s *blockedStore) Load() ([]byte, error) { return s.data, nil }
func (s *blockedStore) Save(data []byte) error {
	if s.armed {
		s.once.Do(func() { close(s.entered) })
		<-s.release
		return errors.New("synthetic uncertain save")
	}
	s.data = append([]byte(nil), data...)
	return nil
}

func newBlockedManager(t *testing.T) (*Manager, *blockedStore, *atomic.Int32) {
	t.Helper()
	vault, err := storage.Open(filepath.Join(t.TempDir(), "private"), &lifecycleKeys{})
	if err != nil {
		t.Fatal(err)
	}
	app, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	local, _ := app.Identity()
	peer, _ := identity.Generate()
	keys := tailcat.NewPrivateKey()
	keys.Public.RegionID = 900
	encoded, _ := keys.Private.MarshalText()
	defer clear(encoded)
	record := storage.PendingRecord{Version: 2, ID: base64.RawURLEncoding.EncodeToString([]byte("synthetic-id-001")), Role: "inviter", Local: local.PublicKey(), Peer: peer.PublicKey(), LocalNodePrivate: string(encoded), LocalNode: keys.Private.Public().String(), PeerNode: key.NewNode().Public().String(), PresharedKey: base64.RawURLEncoding.EncodeToString(keys.Public.PresharedKey[:]), RelayServiceURL: "https://relay.example/map.json", RelayFallbackConsent: true, ServerEndpoint: string(keys.Public.Addr())}
	if app.SavePendingContact(record) != nil {
		t.Fatal("could not prepare synthetic committed state")
	}
	if _, err := app.CommitPendingContact(record); err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	m, err := New(local, app, Factories{Profile: func(context.Context, string) (*transport.RelayProfile, error) {
		calls.Add(1)
		return nil, ErrUnavailable
	}})
	if err != nil {
		t.Fatal(err)
	}
	// This unit fixture injects domain-only local approval and a failing Store.
	// Fresh identity TLS and real transport are covered by separate integrations.
	manager, err := pairing.NewManager(record.Local, pairing.Options{})
	if err != nil {
		t.Fatal(err)
	}
	invite, err := manager.Create(record.ServerEndpoint, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reserve(invite.Link(), record.Peer); err != nil {
		t.Fatal(err)
	}
	contact, err := manager.Approve(record.Peer.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	store := &blockedStore{entered: make(chan struct{}), release: make(chan struct{})}
	// chat.Open needs an absent snapshot rather than invalid zero-length data.
	store.data = nil
	conversation, err := chat.Open(&initiallyEmptyStore{store}, contact, chat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	m.contact, m.conversation = contact, conversation
	store.armed = true
	t.Cleanup(func() {
		store.once.Do(func() { close(store.entered) })
		select {
		case <-store.release:
		default:
			close(store.release)
		}
		m.Close()
		_ = manager.Close()
		_ = app.Close()
	})
	return m, store, calls
}

type initiallyEmptyStore struct{ *blockedStore }

func (s *initiallyEmptyStore) Load() ([]byte, error) {
	if s.data == nil {
		return nil, os.ErrNotExist
	}
	return s.blockedStore.Load()
}

func TestTerminalPersistenceClassificationDoesNotTreatProtocolFailuresAsStorageFailure(t *testing.T) {
	for _, err := range []error{chat.ErrStore, chat.ErrCorrupt, storage.ErrStateFailed, storage.ErrStateConflict, errors.Join(errProtocol, chat.ErrStore)} {
		if !terminalPersistence(err) {
			t.Fatal("terminal local persistence failure would retry transport")
		}
	}
	for _, err := range []error{nil, errProtocol, context.Canceled, chat.ErrFrame} {
		if terminalPersistence(err) {
			t.Fatal("transport/protocol failure misclassified as poisoned local state")
		}
	}
}

func TestCloseJoinsBlockedQueueWriteAndPersistenceFailureDisablesSending(t *testing.T) {
	m, store, calls := newBlockedManager(t)
	sent := make(chan error, 1)
	go func() { _, err := m.Send(m.record.ID, "synthetic bounded queue write"); sent <- err }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("queue did not enter durable save")
	}
	closing := make(chan struct{})
	closed := make(chan struct{})
	go func() { close(closing); m.Close(); close(closed) }()
	<-closing
	select {
	case <-closed:
		t.Fatal("close returned during in-flight queue persistence")
	default:
	}
	close(store.release)
	select {
	case err := <-sent:
		if !errors.Is(err, chat.ErrStore) {
			t.Fatal("uncertain queue save was not propagated")
		}
	case <-time.After(time.Second):
		t.Fatal("queue write did not finish")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close deadlocked with queue/persistence")
	}
	if calls.Load() != 0 {
		t.Fatal("local queue/close started networking")
	}
	status := m.Snapshot()
	if status.State != "unavailable" || status.CanSend || status.CanConnect || len(status.Messages) != 0 {
		t.Fatal("closed manager retained public conversation access")
	}
}

func TestQueuePersistenceFailureDisablesSendConnectAndCancelsRetries(t *testing.T) {
	m, store, calls := newBlockedManager(t)
	result := make(chan error, 1)
	go func() { _, err := m.Send(m.record.ID, "synthetic local persistence failure"); result <- err }()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("queue did not reach persistence")
	}
	close(store.release)
	select {
	case err := <-result:
		if !errors.Is(err, chat.ErrStore) {
			t.Fatal("local persistence error was hidden")
		}
	case <-time.After(time.Second):
		t.Fatal("queue did not return persistence error")
	}
	status := m.Snapshot()
	if status.State != "failed" || status.CanSend || status.CanConnect || len(status.Messages) != 0 {
		t.Fatal("poisoned conversation remained publicly usable")
	}
	if m.ctx.Err() == nil {
		t.Fatal("terminal persistence failure left retry context alive")
	}
	if _, err := m.Connect(m.record.ID); err == nil {
		t.Fatal("poisoned manager accepted reconnect")
	}
	if _, err := m.Send(m.record.ID, "another synthetic message"); err == nil {
		t.Fatal("poisoned manager accepted another send")
	}
	if calls.Load() != 0 {
		t.Fatal("failed local persistence attempted network recovery")
	}
}
