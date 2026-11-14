package desktop

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/messaging"
	"whatsup/internal/transport"
)

func TestDesktopRelayInspectionAndAbsentConsentNeverFetch(t *testing.T) {
	s := unlockedInvitationService(t)
	record := prepareDesktopForgetContact(t, s)
	s.LockStorage()
	var started, retired atomic.Int32
	if err := ConfigureRuntime(s, RuntimeOptions{Factories: messaging.Factories{Profile: func(ctx context.Context, _ string) (*transport.RelayProfile, error) {
		started.Add(1)
		<-ctx.Done()
		retired.Add(1)
		return nil, ctx.Err()
	}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	before := s.GetStatus().IdentityFingerprint
	initial, err := s.GetRelayChange(record.ID)
	if err != nil || initial.CurrentServiceURL != record.RelayServiceURL || initial.State != "idle" || !initial.CanPropose {
		t.Fatal("local relay inspection failed", err)
	}
	if _, err := s.ProposeRelayChange(record.ID, "https://next-relay.example.test/map.json", false); err == nil {
		t.Fatal("absent local consent created a proposal")
	}
	if _, err := s.ProposeRelayChange(record.ID, "http://next-relay.example.test/map.json", true); err == nil {
		t.Fatal("invalid service created a proposal")
	}
	if _, err := s.ImportRelayChange(record.ID, "whatsup-relay:v1:malformed"); err == nil {
		t.Fatal("malformed carry was accepted")
	}
	if got, err := s.GetRelayChange(record.ID); err != nil || got.RequestID != "" || started.Load() != 0 {
		t.Fatal("inspection or rejected input started network work")
	}
	proposal, err := s.ProposeRelayChange(record.ID, "https://next-relay.example.test/map.json", true)
	if err != nil || proposal.RequestID == "" || !proposal.LocalApproved || proposal.PeerApproved {
		t.Fatal("explicit local intent was not saved", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for started.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if started.Load() != 1 {
		t.Fatal("explicit intent did not start exactly one bounded fetch")
	}
	if got, _ := s.GetRelayChange(record.ID); got.Revision != s.messages.Snapshot(record.ID).Revision {
		t.Fatal("relay response and contact hints use different revision clocks")
	}
	if s.LockStorage().StorageState != "locked" || retired.Load() != started.Load() {
		t.Fatal("Go offline did not join relay intent work")
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRelayChange(record.ID)
	if err != nil || got.RequestID != proposal.RequestID || got.CurrentServiceURL != record.RelayServiceURL || s.GetStatus().IdentityFingerprint != before {
		t.Fatal("intent changed the active service, identity or restart context", err)
	}
	if _, err := s.CancelRelayChange(record.ID, proposal.RequestID); err != nil {
		t.Fatal("local pending intent could not be cancelled", err)
	}
}

func TestDesktopForgetNotifiesOnlinePeerAndPreservesItsHistory(t *testing.T) {
	local, peer, incoming := notificationPairFixture(t)
	other := prepareDesktopForgetContact(t, peer)
	before := peer.messages.Snapshot(incoming.ContactID)
	identity := local.GetStatus().IdentityFingerprint
	if _, err := local.ForgetContact(incoming.ContactID); err != nil {
		t.Fatal("local contact removal failed", err)
	}
	got := peer.messages.Snapshot(incoming.ContactID)
	if got.State != "peer-removed" || got.CanSend || got.CanConnect || len(got.Messages) != len(before.Messages) || got.Messages[0].ID != before.Messages[0].ID {
		t.Fatal("authenticated removal lost remote history or left reconnect enabled")
	}
	if got := peer.messages.Snapshot(other.ID); !got.CanSend || got.ContactID != other.ID {
		t.Fatal("removing one contact retired another conversation")
	}
	if local.GetStatus().IdentityFingerprint != identity {
		t.Fatal("Forget replaced the local identity")
	}
	peer.LockStorage()
	if _, err := peer.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	if got := peer.messages.Snapshot(incoming.ContactID); got.State != "peer-removed" || got.CanConnect || got.CanSend || len(got.Messages) != len(before.Messages) {
		t.Fatal("peer removal or history did not survive reopening")
	}
}

func TestDesktopRelayRootWriteFailureLocksAndNotifiesBeforeReopen(t *testing.T) {
	hints := make(chan string, 1)
	s := securityTestService(t, SecurityRuntimeOptions{StorageChanged: func(value string) { hints <- value }})
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	record := prepareDesktopForgetContact(t, s)
	identity := s.GetStatus().IdentityFingerprint
	root := filepath.Join(s.directory, "state.vault")
	kept := root + ".synthetic-kept"
	if err := os.Rename(root, kept); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProposeRelayChange(record.ID, "https://next-relay.example.test/map.json", true); err == nil {
		t.Fatal("failed root write reported a saved proposal")
	}
	if got := s.GetStatus(); got.StorageState != "locked" || got.IdentityFingerprint != "" || got.MessagingAvailable {
		t.Fatal("uncertain policy write retained decrypted or network authority")
	}
	select {
	case hint := <-hints:
		if hint != "locked" {
			t.Fatal("failed root write emitted an incorrect storage hint")
		}
	default:
		t.Fatal("failed root write did not retire renderer permissions")
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(kept, root); err != nil {
		t.Fatal(err)
	}
	if got, err := s.UnlockStorage(); err != nil || got.IdentityFingerprint != identity {
		t.Fatal("preserved root could not reopen with the same identity", err)
	}
	if got, err := s.GetRelayChange(record.ID); err != nil || got.RequestID != "" || got.CurrentServiceURL != record.RelayServiceURL {
		t.Fatal("failed write replaced the confirmed service", err)
	}
}
