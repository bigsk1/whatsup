package transport_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"whatsup/internal/identity"
	"whatsup/internal/pairflow"
	"whatsup/internal/pairing"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

// This provider keeps disposable wrapping credentials in memory. The vault
// encryption and filesystem writes are real; OS credential access is verified
// separately by the platform integration gate.
type disposableIntegrationKeys struct{ value string }

func (keys *disposableIntegrationKeys) Get() (string, error) {
	if keys.value == "" {
		return "", keyring.ErrNotFound
	}
	return keys.value, nil
}
func (keys *disposableIntegrationKeys) Set(value string) error { keys.value = value; return nil }

type encryptedIntegrationPeer struct {
	identity  *identity.Identity
	store     *storage.ApplicationState
	directory string
}

func newEncryptedIntegrationPeer(t *testing.T) encryptedIntegrationPeer {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "private")
	vault, err := storage.Open(directory, &disposableIntegrationKeys{})
	if err != nil {
		t.Fatal("could not create disposable encrypted vault")
	}
	t.Cleanup(func() { _ = vault.Close() })
	app, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal("could not open disposable application envelope")
	}
	t.Cleanup(func() { _ = app.Close() })
	local, err := app.Identity()
	if err != nil {
		t.Fatal("could not read disposable identity")
	}
	return encryptedIntegrationPeer{identity: local, store: app, directory: directory}
}

type liveIntegrationPair struct {
	ctx          context.Context
	owner        encryptedIntegrationPeer
	joiner       encryptedIntegrationPeer
	ownerFlow    *pairflow.Coordinator
	joinerFlow   *pairflow.Coordinator
	manager      *pairing.Manager
	listener     *transport.InvitationListener
	retired      atomic.Int32
	ownerResult  chan error
	joinerResult chan error
}

func newLiveIntegrationPair(t *testing.T) *liveIntegrationPair {
	t.Helper()
	// The only unsafe-address profile constructor exists in _test.go. The
	// disposable relay uses ordinary TLS with an exact certificate fingerprint.
	profile := transport.ExportLoopbackRelayProfileForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	pair := &liveIntegrationPair{ctx: ctx, owner: newEncryptedIntegrationPeer(t), joiner: newEncryptedIntegrationPeer(t),
		ownerResult: make(chan error, 1), joinerResult: make(chan error, 1)}
	t.Cleanup(cancel)
	if pair.owner.identity.PublicKey().Equal(pair.joiner.identity.PublicKey()) {
		t.Fatal("independent vaults generated the same identity")
	}
	var err error
	pair.ownerFlow, err = pairflow.New(pair.owner.identity, pair.owner.store, pairflow.Options{CompleteActivation: true})
	if err != nil {
		t.Fatal("could not create owner workflow")
	}
	pair.joinerFlow, err = pairflow.New(pair.joiner.identity, pair.joiner.store, pairflow.Options{CompleteActivation: true})
	if err != nil {
		t.Fatal("could not create joiner workflow")
	}
	t.Cleanup(func() { cancel(); _ = pair.ownerFlow.Close(); _ = pair.joinerFlow.Close() })
	pair.listener, err = transport.NewInvitationListener(ctx, transport.InvitationOptions{
		Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: true,
	})
	if err != nil {
		t.Fatal("could not start disposable invitation transport")
	}
	t.Cleanup(func() { _ = pair.listener.Close() })
	pair.manager, err = pairing.NewManager(pair.owner.identity.PublicKey(), pairing.Options{CloseInvitation: func() error {
		pair.retired.Add(1)
		return pair.listener.Close()
	}})
	if err != nil {
		t.Fatal("could not create owner invitation manager")
	}
	t.Cleanup(func() { _ = pair.manager.Close() })
	endpoint, err := pair.listener.AddrEndpoint()
	if err != nil {
		t.Fatal("could not obtain private invitation endpoint")
	}
	invite, err := pair.manager.Create(string(endpoint.EncodeForTransport()), 0)
	if err != nil {
		t.Fatal("could not create disposable invitation")
	}
	joinedInvite, err := pairing.ParseInvite(invite.Link())
	if err != nil {
		t.Fatal("could not parse disposable invitation")
	}
	go func() {
		raw, acceptErr := pair.listener.Accept(ctx)
		if acceptErr != nil {
			pair.ownerResult <- acceptErr
			return
		}
		pair.ownerResult <- pair.ownerFlow.Accept(ctx, raw, invite, pair.manager)
	}()
	raw, err := transport.DialInvitation(ctx, joinedInvite.Endpoint(), profile, transport.RelayFallback, true)
	if err != nil {
		t.Fatal("could not dial disposable invitation transport")
	}
	go func() { pair.joinerResult <- pair.joinerFlow.Join(ctx, raw, joinedInvite) }()
	return pair
}

func waitIntegrationApproval(t *testing.T, pair *liveIntegrationPair, flow *pairflow.Coordinator) pairflow.Snapshot {
	t.Helper()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		status := flow.Snapshot()
		if status.State == "awaiting-approval" {
			return status
		}
		if status.State == "failed" || status.State == "expired" || status.State == "unavailable" {
			t.Fatalf("live pairing failed before confirmation; state=%s", status.State)
		}
		select {
		case <-pair.ctx.Done():
			t.Fatalf("live pairing did not reach confirmation; state=%s", status.State)
		case <-tick.C:
		}
	}
}

func waitIntegrationResult(t *testing.T, pair *liveIntegrationPair, side string, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-pair.ctx.Done():
		t.Fatalf("live pairing %s did not finish within its bounded lifetime; owner=%s joiner=%s", side,
			pair.ownerFlow.Snapshot().State, pair.joinerFlow.Snapshot().State)
		return pairflow.ErrProtocol
	}
}

func requireNoIntegrationRecords(t *testing.T, peer encryptedIntegrationPeer) {
	t.Helper()
	pending, err := peer.store.PendingContacts()
	if err != nil || len(pending) != 0 {
		t.Fatal("unapproved pairing retained pending credentials")
	}
	committed, err := peer.store.CommittedContacts()
	if err != nil || len(committed) != 0 {
		t.Fatal("unapproved pairing retained committed credentials")
	}
}

func TestFullPairflowOverRealTailcatCommitsReciprocalEncryptedContacts(t *testing.T) {
	pair := newLiveIntegrationPair(t)
	owner := waitIntegrationApproval(t, pair, pair.ownerFlow)
	joiner := waitIntegrationApproval(t, pair, pair.joinerFlow)
	if pair.manager.Snapshot().State != pairing.Pending || owner.LocalFingerprint != joiner.PeerFingerprint || owner.PeerFingerprint != joiner.LocalFingerprint {
		t.Fatal("fingerprints were not reserved and reciprocal before confirmation")
	}
	requireNoIntegrationRecords(t, pair.owner)
	requireNoIntegrationRecords(t, pair.joiner)
	if _, err := pair.ownerFlow.Approve(owner.RequestID, owner.PeerFingerprint); err != nil {
		t.Fatal("owner explicit approval was rejected")
	}
	if _, err := pair.joinerFlow.Approve(joiner.RequestID, joiner.PeerFingerprint); err != nil {
		t.Fatal("joiner explicit approval was rejected")
	}
	ownerErr := waitIntegrationResult(t, pair, "owner", pair.ownerResult)
	joinerErr := waitIntegrationResult(t, pair, "joiner", pair.joinerResult)
	if ownerErr != nil || joinerErr != nil || pair.ownerFlow.Snapshot().State != "completed" || pair.joinerFlow.Snapshot().State != "completed" {
		t.Fatalf("live pairing did not complete on both sides; owner=%s joiner=%s ownerError=%t joinerError=%t",
			pair.ownerFlow.Snapshot().State, pair.joinerFlow.Snapshot().State, ownerErr != nil, joinerErr != nil)
	}
	left, leftErr := pair.owner.store.CommittedContacts()
	right, rightErr := pair.joiner.store.CommittedContacts()
	if leftErr != nil || rightErr != nil || len(left) != 1 || len(right) != 1 || storage.ValidatePendingRecord(left[0]) != nil || storage.ValidatePendingRecord(right[0]) != nil || left[0].ID != right[0].ID || left[0].PresharedKey != right[0].PresharedKey || left[0].LocalNode != right[0].PeerNode || left[0].PeerNode != right[0].LocalNode || left[0].LocalNodePrivate == right[0].LocalNodePrivate || !left[0].Local.Equal(right[0].Peer) || !left[0].Peer.Equal(right[0].Local) {
		t.Fatal("committed contacts did not contain one exact shared PSK and two reciprocal identity-bound nodes")
	}
	for index, peer := range []encryptedIntegrationPeer{pair.owner, pair.joiner} {
		pending, err := peer.store.PendingContacts()
		if err != nil || len(pending) != 0 {
			t.Fatal("completed pairing retained pending credentials")
		}
		ciphertext, err := os.ReadFile(filepath.Join(peer.directory, "state.vault"))
		record := []storage.PendingRecord{left[0], right[0]}[index]
		seed := peer.identity.Seed()
		if err != nil || bytes.Contains(ciphertext, []byte(record.LocalNodePrivate)) || bytes.Contains(ciphertext, []byte(record.PresharedKey)) || bytes.Contains(ciphertext, seed[:]) {
			t.Fatal("committed contact credentials or identity seed appeared in plaintext persistence")
		}
	}
	if pair.retired.Load() != 1 {
		t.Fatal("completed pairing did not retire the invitation exactly once")
	}
	if _, err := pair.listener.AddrEndpoint(); err == nil {
		t.Fatal("completed pairing retained invitation transport credentials")
	}
}

func TestFullPairflowOverRealTailcatDenialRetiresInvitationWithoutCredentials(t *testing.T) {
	for _, scenario := range []struct {
		name                  string
		joinerDenies          bool
		otherPreviouslyAgreed bool
	}{
		{name: "owner-denies"},
		{name: "joiner-denies", joinerDenies: true},
		{name: "owner-denies-after-joiner-approval", otherPreviouslyAgreed: true},
		{name: "joiner-denies-after-owner-approval", joinerDenies: true, otherPreviouslyAgreed: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pair := newLiveIntegrationPair(t)
			owner := waitIntegrationApproval(t, pair, pair.ownerFlow)
			joiner := waitIntegrationApproval(t, pair, pair.joinerFlow)
			denier, other := pair.ownerFlow, pair.joinerFlow
			denierRequest, otherRequest := owner, joiner
			if scenario.joinerDenies {
				denier, other = other, denier
				denierRequest, otherRequest = otherRequest, denierRequest
			}
			if scenario.otherPreviouslyAgreed {
				if status, err := other.Approve(otherRequest.RequestID, otherRequest.PeerFingerprint); err != nil || status.State != "waiting-for-peer" {
					t.Fatal("single prior approval did not wait for the second person's decision")
				}
			}
			requireNoIntegrationRecords(t, pair.owner)
			requireNoIntegrationRecords(t, pair.joiner)
			if _, err := denier.Deny(denierRequest.RequestID); err != nil {
				t.Fatal("explicit rejection could not retire the live pairing")
			}
			ownerErr := waitIntegrationResult(t, pair, "owner", pair.ownerResult)
			joinerErr := waitIntegrationResult(t, pair, "joiner", pair.joinerResult)
			if ownerErr == nil || joinerErr == nil {
				t.Fatal("denied pairing completed on either side")
			}
			requireNoIntegrationRecords(t, pair.owner)
			requireNoIntegrationRecords(t, pair.joiner)
			if denier.Snapshot().State != "canceled" || other.Snapshot().RequestID != "" || other.Snapshot().State == "completed" || pair.retired.Load() != 1 {
				t.Fatal("denied pairing retained a confirmation request, approval or invitation ownership")
			}
			if _, err := pair.ownerFlow.Approve(owner.RequestID, owner.PeerFingerprint); err == nil {
				t.Fatal("denied owner confirmation remained actionable")
			}
			if _, err := pair.joinerFlow.Approve(joiner.RequestID, joiner.PeerFingerprint); err == nil {
				t.Fatal("denied joiner confirmation remained actionable")
			}
			if _, err := pair.listener.AddrEndpoint(); err == nil {
				t.Fatal("denied pairing retained invitation transport credentials")
			}
		})
	}
}
