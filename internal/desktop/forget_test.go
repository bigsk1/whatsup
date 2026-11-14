package desktop

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"whatsup/internal/identity"
	"whatsup/internal/storage"
)

// This fixture injects local domain approval, never live mutual TLS proof. It
// exercises the encrypted desktop state/lifecycle boundary without contacting a
// relay; actual two-device pairing remains a separate integration/native gate.
type forgetTestPin struct{ record storage.PendingRecord }

func (p forgetTestPin) Local() identity.PublicKey { return p.record.Local }
func (p forgetTestPin) Peer() identity.PublicKey  { return p.record.Peer }
func (p forgetTestPin) ContactID() string         { return p.record.ID }
func (p forgetTestPin) WithAuthenticatedPeer(peer identity.PublicKey, operation func() error) error {
	if !p.record.Peer.Equal(peer) || operation == nil {
		return storage.ErrContact
	}
	return operation()
}

func prepareDesktopForgetContact(t *testing.T, s *Service) storage.PendingRecord {
	t.Helper()
	local, err := s.application.Identity()
	if err != nil {
		t.Fatal(err)
	}
	peer, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	node := tailcat.NewPrivateKey()
	node.Public.RegionID = 900
	encoded, _ := node.Private.MarshalText()
	defer clear(encoded)
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	r := storage.PendingRecord{Version: 2, ID: base64.RawURLEncoding.EncodeToString(id[:]), Role: "inviter", Local: local.PublicKey(), Peer: peer.PublicKey(), LocalNodePrivate: string(encoded), LocalNode: node.Private.Public().String(), PeerNode: key.NewNode().Public().String(), PresharedKey: base64.RawURLEncoding.EncodeToString(node.Public.PresharedKey[:]), RelayServiceURL: disposableRelayURL, RelayFallbackConsent: true, ServerEndpoint: string(node.Public.Addr())}
	if err := s.application.SavePendingContact(r); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.application.CommitPendingContact(r)
	if err != nil || receipt.ConsumeForActivation(r) != nil {
		t.Fatal("could not prepare a consumed desktop contact receipt")
	}
	if err := s.application.ApproveCommittedConversation(r, forgetTestPin{r}); err != nil {
		t.Fatal(err)
	}
	if got := s.GetConversation(); got.ContactID != r.ID || !got.CanSend {
		t.Fatal("synthetic approved local conversation was not opened")
	}
	if got, err := s.SendMessage(r.ID, "synthetic queued message to discard"); err != nil || len(got.Messages) != 1 || got.Messages[0].Delivered {
		t.Fatal("synthetic offline outbox was not durable")
	}
	return r
}

func TestDesktopForgetPreservesIdentityAndEnablesFreshPairingWithoutRestart(t *testing.T) {
	s := unlockedInvitationService(t)
	r := prepareDesktopForgetContact(t, s)
	fingerprint, wrapping := s.GetStatus().IdentityFingerprint, s.keys.(*testKeys).key
	oldMessages, oldPairing, oldInvitations := s.messages, s.pairing, s.invitations
	approval, _ := s.application.LocalConversation(r)
	store, err := s.application.ChatStore(forgetTestPin{r})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ForgetContact(r.ID); err != nil || got.State != "none" || got.ContactID != "" || len(got.Messages) != 0 || got.CanConnect || got.CanSend {
		t.Fatal("forget did not leave an empty disconnected conversation")
	}
	status := s.GetStatus()
	if status.StorageState != "unlocked" || status.IdentityFingerprint != fingerprint || s.keys.(*testKeys).key != wrapping || !status.PairingAvailable {
		t.Fatal("forget changed device identity, credential or unlocked pairing capability")
	}
	if s.messages == oldMessages || s.pairing == oldPairing || s.invitations == oldInvitations {
		t.Fatal("forget reused retired lifecycle controllers")
	}
	if _, err := oldMessages.Connect(r.ID); err == nil {
		t.Fatal("late callback restarted the retired messaging manager")
	}
	if _, err := oldMessages.Send(r.ID, "synthetic stale message"); err == nil || approval.WithLocalApproval(func() error { return nil }) == nil {
		t.Fatal("forget retained a stale runtime or receipt")
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("forget retained an old conversation handle")
	}
	if oldPairing.Snapshot().State != "unavailable" {
		t.Fatal("old pairing controller remained live")
	}
	if _, err := oldInvitations.begin(true, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("old invitation controller admitted new work")
	}
	// Starting a new invitation needs no build, profile reset or unlock. This
	// uses a synthetic loopback listener, not a public service.
	listener := disposableListener(t)
	s.invitations.listen = func(context.Context, string, bool) (invitationListener, error) { return listener, nil }
	share, err := s.CreateInvitation(disposableRelayURL, true)
	if err != nil || share.Link == "" {
		t.Fatal("fresh invitation unavailable after forgetting")
	}
	if _, err := s.CancelInvitation(); err != nil {
		t.Fatal(err)
	}
	replacement := prepareDesktopForgetContact(t, s)
	if replacement.LocalNodePrivate == r.LocalNodePrivate || replacement.PresharedKey == r.PresharedKey || replacement.ID == r.ID {
		t.Fatal("new pairing reused removed contact credentials")
	}
	if got, err := s.ForgetContact(r.ID); err == nil || got.ContactID != replacement.ID || len(got.Messages) != 1 {
		t.Fatal("stale confirmation changed the replacement conversation")
	}
	if err := s.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if status, err := s.UnlockStorage(); err != nil || status.IdentityFingerprint != fingerprint || s.keys.(*testKeys).key != wrapping {
		t.Fatal("device identity or credential changed after forget/reopen")
	}
	if got := s.GetConversation(); got.ContactID != replacement.ID || len(got.Messages) != 1 {
		t.Fatal("replacement association did not survive restart")
	}
}

func TestDesktopForgetRejectsLockedStaleAndMissingRuntime(t *testing.T) {
	for _, scenario := range []string{"locked", "stale", "missing-runtime"} {
		t.Run(scenario, func(t *testing.T) {
			s := unlockedInvitationService(t)
			r := prepareDesktopForgetContact(t, s)
			id := r.ID
			switch scenario {
			case "locked":
				s.LockStorage()
			case "stale":
				id = base64.RawURLEncoding.EncodeToString([]byte("synthetic-id-999"))
			case "missing-runtime":
				s.invitations.close()
				s.invitations = nil
			}
			if _, err := s.ForgetContact(id); err == nil {
				t.Fatal("invalid or unavailable forget request was accepted")
			}
			if scenario == "stale" {
				if got := s.GetConversation(); got.ContactID != r.ID || !got.CanSend || len(got.Messages) != 1 {
					t.Fatal("stale request stopped or changed current conversation")
				}
			} else if s.GetStatus().StorageState == "unlocked" || s.GetConversation().CanSend {
				t.Fatal("missing/locked runtime retained usable chat")
			}
			if _, err := s.UnlockStorage(); err != nil {
				t.Fatal(err)
			}
			if got := s.GetConversation(); got.ContactID != r.ID || len(got.Messages) != 1 {
				t.Fatal("invalid request deleted a durable contact")
			}
		})
	}
}

func TestDesktopForgetRefusedDurableWriteStopsAndPreservesSavedContact(t *testing.T) {
	s := unlockedInvitationService(t)
	r := prepareDesktopForgetContact(t, s)
	oldMessages, oldInvitations, oldPairing := s.messages, s.invitations, s.pairing
	statePath := filepath.Join(s.directory, "state.vault")
	preservedPath := filepath.Join(s.directory, "synthetic-preserved-ciphertext")
	if err := os.Rename(statePath, preservedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ForgetContact(r.ID); err == nil || got.CanSend || got.ContactID != "" || s.GetStatus().StorageState == "unlocked" {
		t.Fatal("refused durable write reported deletion or retained unlocked runtime")
	}
	if _, err := oldMessages.Connect(r.ID); err == nil || oldPairing.Snapshot().State != "unavailable" {
		t.Fatal("refused save retained live controllers")
	}
	if _, err := oldInvitations.begin(true, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("refused save retained invitation capability")
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(preservedPath, statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	if got := s.GetConversation(); got.ContactID != r.ID || len(got.Messages) != 1 {
		t.Fatal("refused durable delete lost saved association/history")
	}
}

func TestDesktopForgetSerializesConcurrentSendAndCannotResurrectDeletedQueue(t *testing.T) {
	s := unlockedInvitationService(t)
	r := prepareDesktopForgetContact(t, s)
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() { defer workers.Done(); <-start; _, _ = s.SendMessage(r.ID, "synthetic racing offline send") }()
	var forgetErr error
	go func() { defer workers.Done(); <-start; _, forgetErr = s.ForgetContact(r.ID) }()
	close(start)
	workers.Wait()
	if forgetErr != nil || s.GetConversation().ContactID != "" {
		t.Fatal("forget did not serialize against concurrent send")
	}
	if _, err := s.SendMessage(r.ID, "synthetic late offline send"); err == nil {
		t.Fatal("late sender resurrected removed contact")
	}
	if err := s.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	if got := s.GetConversation(); got.ContactID != "" || len(got.Messages) != 0 {
		t.Fatal("removed conversation or outbox returned after restart")
	}
}
