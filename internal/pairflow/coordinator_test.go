package pairflow

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
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
	mu      sync.Mutex
	records []storage.PendingRecord
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *memoryStore) SavePendingContact(record storage.PendingRecord) error {
	if s.entered != nil {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if storage.ValidatePendingRecord(record) != nil {
		return storage.ErrPending
	}
	s.records = append(s.records, record)
	return nil
}
func (s *memoryStore) PendingContacts() ([]storage.PendingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storage.PendingRecord{}, s.records...), nil
}
func (s *memoryStore) RemovePendingContact(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, record := range s.records {
		if record.ID == id {
			s.records = append(s.records[:i], s.records[i+1:]...)
			break
		}
	}
	return nil
}

type testPair struct {
	owner, joiner *Coordinator
	ownerStore    Store
	joinerStore   Store
	manager       *pairing.Manager
	retired       atomic.Int32
	ownerResult   chan error
	joinerResult  chan error
	retireFailure atomic.Bool
}

func newTestPair(t *testing.T, ownerStore, joinerStore Store, lifetime time.Duration) *testPair {
	t.Helper()
	owner, _ := identity.Generate()
	joiner, _ := identity.Generate()
	return connectTestPair(t, owner, joiner, ownerStore, joinerStore, lifetime, false)
}

func connectTestPair(t *testing.T, owner, joiner *identity.Identity, ownerStore, joinerStore Store, lifetime time.Duration, wrongBearer bool, complete ...bool) *testPair {
	t.Helper()
	p := &testPair{ownerStore: ownerStore, joinerStore: joinerStore, ownerResult: make(chan error, 1), joinerResult: make(chan error, 1)}
	var err error
	options := Options{CompleteActivation: len(complete) != 0 && complete[0]}
	p.owner, err = New(owner, ownerStore, options)
	if err != nil {
		t.Fatal(err)
	}
	p.joiner, err = New(joiner, joinerStore, options)
	if err != nil {
		t.Fatal(err)
	}
	p.manager, err = pairing.NewManager(owner.PublicKey(), pairing.Options{CloseInvitation: func() error {
		p.retired.Add(1)
		if p.retireFailure.Load() {
			return errors.New("synthetic retirement failure")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	endpoint := tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()}, ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()}, PresharedKey: tailcat.NewPresharedKey(), RegionID: 1}
	invite, err := p.manager.Create(string(endpoint.Addr()), lifetime)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		_ = p.owner.Close()
		_ = p.joiner.Close()
		_ = p.manager.Close()
	})
	go func() {
		raw, err := listener.Accept()
		if err != nil {
			p.ownerResult <- ErrProtocol
			return
		}
		p.ownerResult <- p.owner.Accept(ctx, raw, invite, p.manager)
	}()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	joinedInvite := invite
	if wrongBearer {
		// Change only the canonical bearer, preserving owner/ID/endpoint/expiry.
		// This is synthetic test data and is never printed.
		link := invite.Link()
		for index := range link {
			if index+7 < len(link) && link[index:index+7] == "secret=" {
				value := byte('A')
				if link[index+7] == value {
					value = 'B'
				}
				link = link[:index+7] + string(value) + link[index+8:]
				break
			}
		}
		joinedInvite, err = pairing.ParseInvite(link)
		if err != nil {
			t.Fatal("synthetic bearer mutation was not canonical")
		}
	}
	go func() { p.joinerResult <- p.joiner.Join(ctx, raw, joinedInvite) }()
	return p
}

func waitState(t *testing.T, flow *Coordinator, wanted string) Snapshot {
	t.Helper()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		status := flow.Snapshot()
		if status.State == wanted {
			return status
		}
		select {
		case <-deadline.C:
			t.Fatalf("pairing state did not reach %s; state=%s", wanted, status.State)
		case <-tick.C:
		}
	}
}

func waitRun(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(4 * time.Second):
		t.Fatal("pairing run did not finish")
		return ErrProtocol
	}
}

func approveBoth(t *testing.T, p *testPair) {
	t.Helper()
	owner := waitState(t, p.owner, "awaiting-approval")
	joiner := waitState(t, p.joiner, "awaiting-approval")
	if p.manager.Snapshot().State != pairing.Pending {
		t.Fatal("fingerprint was exposed before bearer reservation")
	}
	if _, err := p.owner.Approve(owner.RequestID, owner.PeerFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := p.joiner.Approve(joiner.RequestID, joiner.PeerFingerprint); err != nil {
		t.Fatal(err)
	}
}

func TestRealTCPReservationsConsentAndInertPendingCredentials(t *testing.T) {
	p := newTestPair(t, &memoryStore{}, &memoryStore{}, 0)
	owner := waitState(t, p.owner, "awaiting-approval")
	joiner := waitState(t, p.joiner, "awaiting-approval")
	if p.manager.Snapshot().State != pairing.Pending || owner.LocalFingerprint != joiner.PeerFingerprint || owner.PeerFingerprint != joiner.LocalFingerprint {
		t.Fatal("confirmation identities were not reserved and reciprocal")
	}
	if _, err := p.owner.Approve("stale", owner.PeerFingerprint); !errors.Is(err, ErrRequest) {
		t.Fatal("stale UI request approved a different attempt")
	}
	if _, err := p.owner.Approve(owner.RequestID, owner.LocalFingerprint); !errors.Is(err, ErrRequest) {
		t.Fatal("wrong fingerprint was confirmed")
	}
	if _, err := p.owner.Approve(owner.RequestID, owner.PeerFingerprint); err != nil {
		t.Fatal(err)
	}
	if p.owner.Snapshot().State != "waiting-for-peer" {
		t.Fatal("local approval did not wait for peer")
	}
	for _, store := range []Store{p.ownerStore, p.joinerStore} {
		if records, err := store.PendingContacts(); err != nil || len(records) != 0 {
			t.Fatal("credentials were persisted before mutual consent")
		}
	}
	if _, err := p.joiner.Approve(joiner.RequestID, joiner.PeerFingerprint); err != nil {
		t.Fatal(err)
	}
	if waitRun(t, p.ownerResult) != nil || waitRun(t, p.joinerResult) != nil {
		t.Fatal("reserved mutual setup failed")
	}
	left, _ := p.ownerStore.PendingContacts()
	right, _ := p.joinerStore.PendingContacts()
	if len(left) != 1 || len(right) != 1 || left[0].ID != right[0].ID || left[0].PresharedKey != right[0].PresharedKey || left[0].LocalNode != right[0].PeerNode || left[0].PeerNode != right[0].LocalNode || left[0].LocalNodePrivate == right[0].LocalNodePrivate {
		t.Fatal("pending credentials are not one shared PSK with distinct bound node keys")
	}
	if p.owner.Snapshot().State != "pending" || p.joiner.Snapshot().State != "pending" || p.retired.Load() != 1 || p.manager.Snapshot().State != pairing.Revoked {
		t.Fatal("pending setup was mislabeled active or invitation transport survived")
	}
	if _, err := p.owner.Approve(owner.RequestID, owner.PeerFingerprint); !errors.Is(err, ErrRequest) {
		t.Fatal("saved pending setup granted another approval")
	}
	if err := p.owner.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(p.owner.local, p.ownerStore)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	status := restored.Snapshot()
	if status.State != "pending" || status.ExpiresAt != "" || status.RequestID == owner.RequestID {
		t.Fatal("restore resumed an invitation or reused an old UI request")
	}
	if _, err := restored.Deny(status.RequestID); err != nil {
		t.Fatal(err)
	}
	if records, _ := p.ownerStore.PendingContacts(); len(records) != 0 {
		t.Fatal("explicit discard did not remove inert pending credentials")
	}
}

func TestWrongBearerNeverPresentsEitherFingerprint(t *testing.T) {
	owner, _ := identity.Generate()
	joiner, _ := identity.Generate()
	p := connectTestPair(t, owner, joiner, &memoryStore{}, &memoryStore{}, 0, true)
	if waitRun(t, p.ownerResult) == nil || waitRun(t, p.joinerResult) == nil {
		t.Fatal("wrong bearer completed reserved setup")
	}
	for _, flow := range []*Coordinator{p.owner, p.joiner} {
		status := flow.Snapshot()
		if status.LocalFingerprint != "" || status.PeerFingerprint != "" || status.RequestID != "" {
			t.Fatal("failed reservation presented an identity confirmation")
		}
	}
}

func TestDenyDismissesPeerWithoutAnyPeerDecision(t *testing.T) {
	for _, ownerDenies := range []bool{true, false} {
		t.Run(map[bool]string{true: "owner", false: "joiner"}[ownerDenies], func(t *testing.T) {
			p := newTestPair(t, &memoryStore{}, &memoryStore{}, 0)
			owner := waitState(t, p.owner, "awaiting-approval")
			joiner := waitState(t, p.joiner, "awaiting-approval")
			flow, id := p.joiner, joiner.RequestID
			if ownerDenies {
				flow, id = p.owner, owner.RequestID
			}
			if _, err := flow.Deny(id); err != nil {
				t.Fatal(err)
			}
			if waitRun(t, p.ownerResult) == nil || waitRun(t, p.joinerResult) == nil || p.retired.Load() != 1 {
				t.Fatal("denied session survived without another user action")
			}
			for _, other := range []*Coordinator{p.owner, p.joiner} {
				if other.Snapshot().PeerFingerprint != "" {
					t.Fatal("closed invitation retained the confirmation prompt")
				}
			}
		})
	}
}

func TestCancelJoinsInFlightSaveBeforeRemovingPendingRecord(t *testing.T) {
	store := &memoryStore{entered: make(chan struct{}), release: make(chan struct{})}
	p := newTestPair(t, store, &memoryStore{}, 0)
	approveBoth(t, p)
	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("pending save was never entered")
	}
	status := p.owner.Snapshot()
	discarded := make(chan error, 1)
	go func() { _, err := p.owner.Deny(status.RequestID); discarded <- err }()
	waitState(t, p.owner, "canceled")
	select {
	case <-discarded:
		t.Fatal("discard returned while a stale save could still resurrect its record")
	default:
	}
	close(store.release)
	select {
	case err := <-discarded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("discard did not join and clean up persistence")
	}
	if records, _ := store.PendingContacts(); len(records) != 0 {
		t.Fatal("canceled record was resurrected by late persistence")
	}
	waitRun(t, p.ownerResult)
	waitRun(t, p.joinerResult)
}

func TestExpiryAndShutdownRetirePendingSession(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "expiry", true: "shutdown"}[shutdown], func(t *testing.T) {
			lifetime := 2 * time.Second
			if shutdown {
				lifetime = 0
			}
			p := newTestPair(t, &memoryStore{}, &memoryStore{}, lifetime)
			waitState(t, p.owner, "awaiting-approval")
			waitState(t, p.joiner, "awaiting-approval")
			if shutdown {
				if err := p.owner.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if waitRun(t, p.ownerResult) == nil || waitRun(t, p.joinerResult) == nil || p.retired.Load() != 1 {
				t.Fatal("terminal lifecycle did not close both sides and retire once")
			}
			if p.owner.Snapshot().PeerFingerprint != "" || p.joiner.Snapshot().PeerFingerprint != "" {
				t.Fatal("expired or stopped pairing retained a fingerprint prompt")
			}
		})
	}
}

type disposableKeys struct{ value string }

func (k *disposableKeys) Get() (string, error) {
	if k.value == "" {
		return "", keyring.ErrNotFound
	}
	return k.value, nil
}
func (k *disposableKeys) Set(value string) error { k.value = value; return nil }

func encryptedPeer(t *testing.T) (*identity.Identity, *storage.ApplicationState, string, *disposableKeys) {
	t.Helper()
	directory, keys := filepath.Join(t.TempDir(), "private"), &disposableKeys{}
	vault, err := storage.Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	app, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	local, err := app.Identity()
	if err != nil {
		t.Fatal(err)
	}
	return local, app, directory, keys
}

func TestCoordinatorPersistsOnlyEncryptedPendingAndReopensInert(t *testing.T) {
	owner, ownerApp, directory, keys := encryptedPeer(t)
	joiner, joinerApp, _, _ := encryptedPeer(t)
	p := connectTestPair(t, owner, joiner, ownerApp, joinerApp, 0, false)
	approveBoth(t, p)
	if waitRun(t, p.ownerResult) != nil || waitRun(t, p.joinerResult) != nil {
		t.Fatal("encrypted pending exchange did not complete")
	}
	records, err := ownerApp.PendingContacts()
	if err != nil || len(records) != 1 {
		t.Fatal("encrypted coordinator did not save one pending record")
	}
	ciphertext, err := os.ReadFile(filepath.Join(directory, "state.vault"))
	if err != nil || bytes.Contains(ciphertext, []byte(records[0].LocalNodePrivate)) || bytes.Contains(ciphertext, []byte(records[0].PresharedKey)) {
		t.Fatal("pending credentials appeared in plaintext persistence")
	}
	if err := p.owner.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ownerApp.Close(); err != nil {
		t.Fatal(err)
	}
	vault, err := storage.Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer vault.Close()
	reopened, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal(err)
	}
	local, err := reopened.Identity()
	if err != nil || !local.PublicKey().Equal(owner.PublicKey()) {
		t.Fatal("pending schema changed the local identity")
	}
	restored, err := New(local, reopened)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	status := restored.Snapshot()
	if status.State != "pending" || status.ExpiresAt != "" {
		t.Fatal("encrypted reopen resumed a live invitation")
	}
	if _, err := restored.Approve(status.RequestID, status.PeerFingerprint); !errors.Is(err, ErrRequest) {
		t.Fatal("restored pending record granted a live approval")
	}
}

func TestSecondCandidateCannotRetireTheReservedManager(t *testing.T) {
	p := newTestPair(t, &memoryStore{}, &memoryStore{}, 0)
	status := waitState(t, p.owner, "awaiting-approval")
	waitState(t, p.joiner, "awaiting-approval")
	p.owner.mu.Lock()
	invite := p.owner.current.invite
	p.owner.mu.Unlock()
	left, right := net.Pipe()
	defer right.Close()
	if err := p.owner.Accept(context.Background(), left, invite, p.manager); !errors.Is(err, ErrUnavailable) {
		t.Fatal("second candidate bypassed current request")
	}
	if p.retired.Load() != 0 || p.manager.Snapshot().State != pairing.Pending || p.owner.Snapshot().RequestID != status.RequestID {
		t.Fatal("rejected candidate retired another candidate's reserved invitation")
	}
	if _, err := p.owner.Deny(status.RequestID); err != nil {
		t.Fatal(err)
	}
	waitRun(t, p.ownerResult)
	waitRun(t, p.joinerResult)
}

func TestShutdownJoinsPendingWriteBeforeVaultCanClose(t *testing.T) {
	store := &memoryStore{entered: make(chan struct{}), release: make(chan struct{})}
	p := newTestPair(t, store, &memoryStore{}, 0)
	approveBoth(t, p)
	select {
	case <-store.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("pending write was never entered")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- p.owner.Close() }()
	waitState(t, p.owner, "unavailable")
	select {
	case <-stopped:
		t.Fatal("shutdown returned before the pending writer quiesced")
	default:
	}
	close(store.release)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not join the pending writer")
	}
	if records, _ := store.PendingContacts(); len(records) != 1 {
		t.Fatal("shutdown silently removed an uncertain inert record")
	}
	waitRun(t, p.ownerResult)
	waitRun(t, p.joinerResult)
}
