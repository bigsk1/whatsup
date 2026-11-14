package pairing

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/pairing/channel"
)

// This test deliberately uses a real loopback TCP socket and TLS. It does not
// start Tailcat or prove WireGuard, relay, NAT, two-device or desktop integration.
func authenticatedPair(t *testing.T, owner, candidate *identity.Identity, invite Invite) (*channel.Session, *channel.Session) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	type result struct {
		session *channel.Session
		err     error
	}
	accepted := make(chan result, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			accepted <- result{err: err}
			return
		}
		session, err := channel.NewServer(ctx, connection, owner, invite.ChannelID(), invite.ExpiresAt(), invite.ContextBinding())
		accepted <- result{session, err}
	}()
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	joined, err := channel.NewClient(ctx, connection, candidate, owner.PublicKey(), invite.ChannelID(), invite.ExpiresAt(), invite.ContextBinding())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = joined.Close() })
	select {
	case got := <-accepted:
		if got.err != nil {
			t.Fatal(got.err)
		}
		t.Cleanup(func() { _ = got.session.Close() })
		return got.session, joined
	case <-ctx.Done():
		t.Fatal("loopback pairing handshake did not finish")
		return nil, nil
	}
}

func authenticatedIdentities(t *testing.T) (*identity.Identity, *identity.Identity) {
	t.Helper()
	owner, err := identity.FromSeed([]byte(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := identity.FromSeed([]byte(strings.Repeat("b", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return owner, candidate
}

func TestAuthenticatedInvitationReservationAndMutualConsentStayUnactivated(t *testing.T) {
	owner, candidate := authenticatedIdentities(t)
	var retired atomic.Int32
	var invitationChannel *channel.Session
	manager, err := NewManager(owner.PublicKey(), Options{CloseInvitation: func() error {
		retired.Add(1)
		if invitationChannel != nil {
			return invitationChannel.Close()
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	invite, err := manager.Create(syntheticEndpoint(), 0)
	if err != nil {
		t.Fatal(err)
	}
	inviter, joiner := authenticatedPair(t, owner, candidate, invite)
	invitationChannel = inviter
	var winners atomic.Int32
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := manager.ReserveAuthenticated(invite.Link(), inviter); err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrBusy) {
				t.Errorf("unexpected reservation failure: %v", err)
			}
		}()
	}
	group.Wait()
	if winners.Load() != 1 || manager.Snapshot().CandidateFingerprint != candidate.PublicKey().Fingerprint() {
		t.Fatal("TLS-authenticated candidate did not reserve the invitation exactly once")
	}
	if contact, err := manager.Approve(candidate.PublicKey().Fingerprint()); contact != nil || !errors.Is(err, ErrActivation) {
		t.Fatal("local approval bypassed the unfinished durable activation gate")
	}
	if retired.Load() != 0 || manager.Snapshot().State != Pending {
		t.Fatal("pending pairing prematurely retired its credential exchange channel")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type consent struct {
		proof *channel.Approval
		err   error
	}
	remote := make(chan consent, 1)
	go func() {
		proof, err := joiner.Approve(ctx, owner.PublicKey().Fingerprint())
		remote <- consent{proof, err}
	}()
	proof, err := inviter.Approve(ctx, candidate.PublicKey().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	other := <-remote
	if other.err != nil || !proof.MatchesSession(inviter) || proof.MatchesSession(joiner) || !other.proof.MatchesSession(joiner) {
		t.Fatal("mutual consent was not bound to the correct TLS session")
	}
	if err := proof.WithLiveApproval(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if contact, err := manager.Approve(candidate.PublicKey().Fingerprint()); contact != nil || !errors.Is(err, ErrActivation) || retired.Load() != 0 {
		t.Fatal("mutual consent alone granted authority or cut off invitation transport")
	}
	if err := manager.Revoke(); err != nil || retired.Load() != 1 {
		t.Fatal("revocation failed to retire the invitation exactly once")
	}
	if err := proof.WithLiveApproval(func() error { t.Fatal("closed proof admitted an operation"); return nil }); err == nil {
		t.Fatal("closed approval proof retained authority")
	}
	if err := manager.Close(); err != nil || retired.Load() != 1 {
		t.Fatal("shutdown repeated invitation retirement")
	}
}

func TestAuthenticatedReservationRejectsWrongContextAndClosedChannels(t *testing.T) {
	owner, candidate := authenticatedIdentities(t)
	manager, err := NewManager(owner.PublicKey(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	invite, err := manager.Create(syntheticEndpoint(), 0)
	if err != nil {
		t.Fatal(err)
	}
	inviter, joiner := authenticatedPair(t, owner, candidate, invite)
	for _, session := range []*channel.Session{nil, {}, joiner} {
		if _, err := manager.ReserveAuthenticated(invite.Link(), session); !errors.Is(err, ErrChannel) {
			t.Fatal("missing, forged or wrong-role channel was accepted")
		}
	}
	otherInvite := invite
	otherInvite.id[0] ^= 1
	if _, err := manager.ReserveAuthenticated(otherInvite.Link(), inviter); !errors.Is(err, ErrChannel) {
		t.Fatal("authenticated channel was reused for another invitation with the same owner and expiry")
	}
	otherInvite = invite
	otherInvite.expires = otherInvite.expires.Add(-time.Second)
	if _, err := manager.ReserveAuthenticated(otherInvite.Link(), inviter); !errors.Is(err, ErrChannel) {
		t.Fatal("authenticated channel was reused for another invitation expiry")
	}
	otherInvite = invite
	otherInvite.secret[0] ^= 1
	if _, err := manager.ReserveAuthenticated(otherInvite.Link(), inviter); !errors.Is(err, ErrInvalidInvite) {
		t.Fatal("TLS authentication replaced bearer-secret verification")
	}
	if manager.Snapshot().State != Created {
		t.Fatal("invalid channel context changed invitation state")
	}
	if err := inviter.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ReserveAuthenticated(invite.Link(), inviter); !errors.Is(err, ErrChannel) {
		t.Fatal("closed authentication channel reserved an invitation")
	}
}
