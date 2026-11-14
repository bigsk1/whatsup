package desktop

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"whatsup/internal/pairflow"
	"whatsup/internal/storage"
	"whatsup/internal/transport/address"
)

const disposableRelayURL = "https://relay.example.invalid/map.json"

type failedDiscardStore struct{ *storage.ApplicationState }

func (failedDiscardStore) RemoveCommittedContact(string) error { return storage.ErrPending }

type localInvitationListener struct {
	net.Listener
	endpoint address.Endpoint
	once     sync.Once
	closed   atomic.Int32
}

func (l *localInvitationListener) AddrEndpoint() (address.Endpoint, error) { return l.endpoint, nil }
func (l *localInvitationListener) Accept(context.Context) (net.Conn, error) {
	return l.Listener.Accept()
}
func (l *localInvitationListener) Close() error {
	l.once.Do(func() { l.closed.Add(1); _ = l.Listener.Close() })
	return nil
}

func disposableListener(t *testing.T) *localInvitationListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	info := tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()}, ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()}, PresharedKey: tailcat.NewPresharedKey(), RegionID: 1}
	endpoint, err := address.Parse(string(info.Addr()))
	if err != nil {
		t.Fatal("invalid disposable endpoint")
	}
	owned := &localInvitationListener{Listener: l, endpoint: endpoint}
	t.Cleanup(func() { _ = owned.Close() })
	return owned
}

func unlockedInvitationService(t *testing.T) *Service {
	t.Helper()
	s := New(filepath.Join(t.TempDir(), "private"), &testKeys{})
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ServiceShutdown() })
	return s
}

func waitDesktopPairing(t *testing.T, s *Service, expected string) pairflow.Snapshot {
	t.Helper()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	limit := time.NewTimer(4 * time.Second)
	defer limit.Stop()
	for {
		status := s.GetPairingStatus()
		if status.State == expected {
			return status
		}
		select {
		case <-tick.C:
		case <-limit.C:
			t.Fatalf("desktop pairing did not reach expected finite state: %s", status.State)
		}
	}
}

func TestInvitationConsentPrecedesFactoriesAndUnlockStartsNoNetwork(t *testing.T) {
	s := unlockedInvitationService(t)
	var calls atomic.Int32
	s.invitations.listen = func(context.Context, string, bool) (invitationListener, error) {
		calls.Add(1)
		return nil, net.ErrClosed
	}
	s.invitations.dial = func(context.Context, address.Endpoint, string, bool) (net.Conn, error) {
		calls.Add(1)
		return nil, net.ErrClosed
	}
	if share, err := s.CreateInvitation(disposableRelayURL, false); err == nil || share.Link != "" {
		t.Fatal("unconsented invitation creation was accepted")
	}
	if _, err := s.JoinInvitation("invalid", disposableRelayURL, true); err == nil {
		t.Fatal("malformed invitation was accepted")
	}
	s.GetStatus()
	s.GetPairingStatus()
	s.LockStorage()
	if _, err := s.CreateInvitation(disposableRelayURL, true); err == nil {
		t.Fatal("locked storage allowed creation")
	}
	if _, err := s.JoinInvitation("invalid", disposableRelayURL, true); err == nil {
		t.Fatal("locked storage allowed joining")
	}
	if calls.Load() != 0 {
		t.Fatal("unlock, status, lock or rejected actions reached network factories")
	}
}

func TestInvitationLockJoinsCreationAndReturnsNoStaleBearer(t *testing.T) {
	s := unlockedInvitationService(t)
	entered := make(chan struct{})
	l := disposableListener(t)
	s.invitations.listen = func(ctx context.Context, _ string, _ bool) (invitationListener, error) {
		close(entered)
		<-ctx.Done()
		return l, nil // A late successful factory cannot resurrect its job.
	}
	result := make(chan InvitationShare, 1)
	go func() { share, _ := s.CreateInvitation(disposableRelayURL, true); result <- share }()
	<-entered
	if s.LockStorage().StorageState != "locked" {
		t.Fatal("lock did not join in-flight creation")
	}
	select {
	case share := <-result:
		if share.Link != "" || share.ExpiresAt != "" || l.closed.Load() != 1 {
			t.Fatal("late creation returned a bearer or retained its listener after lock")
		}
	case <-time.After(time.Second):
		t.Fatal("creation survived storage lock")
	}
}

func TestDesktopInvitationActionsCompleteAndReopenInert(t *testing.T) {
	owner, joiner := unlockedInvitationService(t), unlockedInvitationService(t)
	l := disposableListener(t)
	owner.invitations.listen = func(context.Context, string, bool) (invitationListener, error) { return l, nil }
	joiner.invitations.dial = func(ctx context.Context, endpoint address.Endpoint, url string, consent bool) (net.Conn, error) {
		if !consent || url != disposableRelayURL || !endpoint.Equal(l.endpoint) {
			return nil, net.ErrClosed
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", l.Addr().String())
	}
	share, err := owner.CreateInvitation(disposableRelayURL, true)
	if err != nil || share.Link == "" {
		t.Fatal("explicit create did not return private share")
	}
	if _, err := joiner.JoinInvitation(share.Link, disposableRelayURL, false); err == nil {
		t.Fatal("unconsented valid invitation reached dial")
	}
	if _, err := joiner.JoinInvitation(share.Link, disposableRelayURL, true); err != nil {
		t.Fatal("explicit join rejected disposable invitation")
	}
	left := waitDesktopPairing(t, owner, "awaiting-approval")
	right := waitDesktopPairing(t, joiner, "awaiting-approval")
	if left.LocalFingerprint != right.PeerFingerprint || right.LocalFingerprint != left.PeerFingerprint {
		t.Fatal("reserved desktop confirmation identities were not reciprocal")
	}
	for _, s := range []*Service{owner, joiner} {
		public, _ := json.Marshal(s.GetPairingStatus())
		if strings.Contains(string(public), share.Link) || strings.Contains(string(public), "secret=") {
			t.Fatal("status polling leaked invitation bearer")
		}
		status := s.GetPairingStatus()
		if _, err := s.ApprovePairing(status.RequestID, status.PeerFingerprint); err != nil {
			t.Fatal("reserved desktop approval was rejected")
		}
	}
	waitDesktopPairing(t, owner, "completed")
	waitDesktopPairing(t, joiner, "completed")
	if l.closed.Load() != 1 || owner.GetStatus().MessagingAvailable || joiner.GetStatus().MessagingAvailable {
		t.Fatal("completion retained invitation or enabled messaging")
	}
	for _, s := range []*Service{owner, joiner} {
		s.LockStorage()
		if _, err := s.UnlockStorage(); err != nil {
			t.Fatal("committed contact prevented storage reopen")
		}
		status := s.GetPairingStatus()
		if status.State != "reconnect-required" || status.ExpiresAt != "" {
			t.Fatal("reopen revived a live approval")
		}
		if _, err := s.ApprovePairing(status.RequestID, status.PeerFingerprint); err == nil {
			t.Fatal("restored contact recreated consent")
		}
		if s == owner {
			// Failed durable removal must reject every cancellation attempt,
			// preserve a discardable inert request and retain the exact record.
			s.invitations.close()
			_ = s.pairing.Close()
			local, _ := s.application.Identity()
			flow, err := pairflow.New(local, failedDiscardStore{s.application}, pairflow.Options{CompleteActivation: true})
			if err != nil {
				t.Fatal(err)
			}
			s.pairing, s.invitations = flow, newInvitationControl(local, flow)
			for range 2 {
				status, err := s.CancelInvitation()
				if err == nil || status.State != "reconnect-required" || status.RequestID == "" {
					t.Fatal("failed discard was falsely confirmed or lost its inert request")
				}
			}
			records, _ := s.application.CommittedContacts()
			if len(records) != 1 {
				t.Fatal("failed discard changed the encrypted record")
			}
			s.invitations.close()
			_ = flow.Close()
			flow, err = pairflow.New(local, s.application, pairflow.Options{CompleteActivation: true})
			if err != nil {
				t.Fatal(err)
			}
			s.pairing, s.invitations = flow, newInvitationControl(local, flow)
		}
		if _, err := s.CancelInvitation(); err != nil {
			t.Fatal("inert committed setup could not be discarded")
		}
		records, err := s.application.CommittedContacts()
		if err != nil || len(records) != 0 || s.GetPairingStatus().State != "canceled" {
			t.Fatal("cancel did not discard inert committed setup")
		}
	}
}

func TestCancelInvitationJoinsPendingAccept(t *testing.T) {
	s := unlockedInvitationService(t)
	l := disposableListener(t)
	s.invitations.listen = func(context.Context, string, bool) (invitationListener, error) { return l, nil }
	if _, err := s.CreateInvitation(disposableRelayURL, true); err != nil {
		t.Fatal("could not create pending accept")
	}
	s.CancelInvitation()
	if l.closed.Load() != 1 || s.GetPairingStatus().State == "connecting" {
		t.Fatal("canceled invitation retained a background accept")
	}
	// The old job is joined, so a new explicit action can reach a fresh factory.
	var reached bool
	s.invitations.listen = func(context.Context, string, bool) (invitationListener, error) {
		reached = true
		return nil, net.ErrClosed
	}
	_, _ = s.CreateInvitation(disposableRelayURL, true)
	if !reached {
		t.Fatal("cancellation retained the old invitation job")
	}
}
