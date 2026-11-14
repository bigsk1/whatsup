package pairflow

import (
	"errors"
	"testing"

	"whatsup/internal/pairing"
	"whatsup/internal/storage"
)

func TestCompletedActivationUsesExactAgreedCredentialsAndDoesNotRestoreAuthority(t *testing.T) {
	owner, ownerApp, _, _ := encryptedPeer(t)
	joiner, joinerApp, _, _ := encryptedPeer(t)
	p := connectTestPair(t, owner, joiner, ownerApp, joinerApp, 0, false, true)
	waitState(t, p.owner, "awaiting-approval")
	p.owner.mu.Lock()
	completedAttempt := p.owner.current
	p.owner.mu.Unlock()
	approveBoth(t, p)
	if waitRun(t, p.ownerResult) != nil || waitRun(t, p.joinerResult) != nil {
		t.Fatal("mutual durable activation failed")
	}
	if p.owner.Snapshot().State != "completed" || p.joiner.Snapshot().State != "completed" || p.manager.Snapshot().State != pairing.Consumed || p.retired.Load() != 1 {
		t.Fatal("activation was published before successful invitation retirement")
	}
	left, err := ownerApp.CommittedContacts()
	if err != nil {
		t.Fatal(err)
	}
	right, err := joinerApp.CommittedContacts()
	if err != nil || len(left) != 1 || len(right) != 1 || left[0].ID != right[0].ID || left[0].PresharedKey != right[0].PresharedKey || left[0].LocalNode != right[0].PeerNode {
		t.Fatal("completed records replaced the agreed key association")
	}
	if pending, _ := ownerApp.PendingContacts(); len(pending) != 0 {
		t.Fatal("committed activation retained pending state")
	}
	if completedAttempt.contact.AuthorizeAuthenticatedPeer(joiner.PublicKey()) == nil {
		t.Fatal("completed pairing retained temporary contact authority")
	}
	if err := p.owner.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := New(owner, ownerApp, Options{CompleteActivation: true})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	status := restored.Snapshot()
	if status.State != "reconnect-required" || status.ExpiresAt != "" {
		t.Fatal("committed storage alone restored live authority")
	}
	if _, err := restored.Approve(status.RequestID, status.PeerFingerprint); !errors.Is(err, ErrRequest) {
		t.Fatal("restored committed record reused invitation approval")
	}
}

func TestFailedInvitationRetirementPublishesNoInviterCapability(t *testing.T) {
	owner, ownerApp, _, _ := encryptedPeer(t)
	joiner, joinerApp, _, _ := encryptedPeer(t)
	p := connectTestPair(t, owner, joiner, ownerApp, joinerApp, 0, false, true)
	p.retireFailure.Store(true)
	approveBoth(t, p)
	if waitRun(t, p.ownerResult) == nil {
		t.Fatal("failed retirement activated inviter")
	}
	waitRun(t, p.joinerResult)
	if p.owner.Snapshot().State != "reconnect-required" || p.manager.Snapshot().State != pairing.Revoked {
		t.Fatal("failed retirement leaked runtime authority")
	}
	records, err := ownerApp.CommittedContacts()
	if err != nil || len(records) != 1 {
		t.Fatal("interrupted completion lost the exact durable record")
	}
	status := p.owner.Snapshot()
	if _, err := p.owner.Deny(status.RequestID); err != nil {
		t.Fatal(err)
	}
	records, _ = ownerApp.CommittedContacts()
	if len(records) != 0 {
		t.Fatal("inert failed activation could not be explicitly discarded")
	}
}

// Compile-time proof that the application store supplies the completion scope;
// generic local pins and pending-record values do not implement this capability.
var _ completionStore = (*storage.ApplicationState)(nil)
