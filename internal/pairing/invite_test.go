package pairing

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"

	"whatsup/internal/identity"
	"whatsup/internal/transport/address"
)

func syntheticEndpoint() string {
	ci := tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()},
		PresharedKey:      tailcat.NewPresharedKey(), RegionID: 1,
	}
	return string(ci.Addr())
}

func syntheticKey(t *testing.T, fill byte) identity.PublicKey {
	t.Helper()
	key, err := identity.FromSeed([]byte(strings.Repeat(string([]byte{fill}), 32)))
	if err != nil {
		t.Fatal(err)
	}
	return key.PublicKey()
}

func managerForTest(t *testing.T, now func() time.Time, close func() error) (*Manager, Invite) {
	t.Helper()
	manager, err := NewManager(syntheticKey(t, 1), Options{Now: now, CloseInvitation: close})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	invite, err := manager.Create(syntheticEndpoint(), 0)
	if err != nil {
		t.Fatal(err)
	}
	return manager, invite
}

func TestSingleUseConcurrentRedemption(t *testing.T) {
	manager, invite := managerForTest(t, nil, nil)
	const candidates = 64
	var winners atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < candidates; i++ {
		candidate := syntheticKey(t, byte(i+2))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := manager.Reserve(invite.Link(), candidate); err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrBusy) {
				t.Errorf("unexpected redemption error: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if winners.Load() != 1 || manager.Snapshot().State != Pending {
		t.Fatal("concurrent redemption did not have exactly one winner")
	}
	contact, err := manager.Approve(manager.Snapshot().CandidateFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer contact.Revoke()
	if _, err := manager.Reserve(invite.Link(), contact.Peer()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("consumed invitation was replayed")
	}
	if _, err := manager.Approve(contact.Peer().Fingerprint()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("approval replayed")
	}
	if manager.invite.secret != [32]byte{} || manager.invite.endpoint.Valid() {
		t.Fatal("invitation credentials retained after consumption")
	}
}

func TestApprovalBindsExactIdentityAndClosesInvitation(t *testing.T) {
	var closes atomic.Int32
	manager, invite := managerForTest(t, nil, func() error { closes.Add(1); return nil })
	peer := syntheticKey(t, 2)
	if _, err := manager.Approve(peer.Fingerprint()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unreserved identity approved")
	}
	snapshot, err := manager.Reserve(invite.Link(), peer)
	if err != nil || snapshot.CandidateFingerprint != peer.Fingerprint() {
		t.Fatalf("reservation failed: %v", err)
	}
	for _, wrong := range []string{"Alice", peer.Fingerprint()[:32], strings.ToUpper(peer.Fingerprint()), syntheticKey(t, 3).Fingerprint()} {
		if _, err := manager.Approve(wrong); !errors.Is(err, ErrFingerprint) {
			t.Fatal("wrong fingerprint accepted")
		}
	}
	contact, err := manager.Approve(peer.Fingerprint())
	if err != nil || closes.Load() != 1 {
		t.Fatalf("approval did not retire invitation: %v", err)
	}
	if contact.Local() != invite.Owner() || contact.Peer() != peer {
		t.Fatal("contact identity binding changed")
	}
	if err := contact.AuthorizeAuthenticatedPeer(peer); err != nil {
		t.Fatal("approved authenticated peer rejected")
	}
	if err := contact.AuthorizeAuthenticatedPeer(syntheticKey(t, 3)); !errors.Is(err, ErrPeer) {
		t.Fatal("third peer admitted")
	}
	encoded, _ := json.Marshal(contact)
	if string(encoded) != "{}" || fmt.Sprintf("%#v", contact) != "[private WhatsUp contact credentials]" {
		t.Fatal("contact credential serialized")
	}
	contact.Revoke()
	if err := contact.AuthorizeAuthenticatedPeer(peer); !errors.Is(err, ErrPeer) {
		t.Fatal("revoked contact authorized")
	}
	if err := contact.PrepareTransportCredentials(func() error { return nil }); !errors.Is(err, ErrPeer) {
		t.Fatal("revoked contact prepared credentials")
	}
	_ = manager.Close()
	if closes.Load() != 1 {
		t.Fatal("invitation listener closed more than once")
	}
}

func TestServerSideExpiryAtBoundaryAlsoExpiresPending(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%v", pending), func(t *testing.T) {
			var elapsed atomic.Int64
			base := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
			now := func() time.Time { return base.Add(time.Duration(elapsed.Load())) }
			var closes atomic.Int32
			manager, invite := managerForTest(t, now, func() error { closes.Add(1); return nil })
			if !invite.ExpiresAt().Equal(base.Add(DefaultLifetime)) {
				t.Fatal("unexpected default expiry")
			}
			peer := syntheticKey(t, 2)
			if pending {
				if _, err := manager.Reserve(invite.Link(), peer); err != nil {
					t.Fatal(err)
				}
			}
			elapsed.Store(int64(DefaultLifetime))
			if _, err := manager.Reserve(invite.Link(), peer); !errors.Is(err, ErrExpired) {
				t.Fatalf("expired invite accepted: %v", err)
			}
			if _, err := manager.Approve(peer.Fingerprint()); !errors.Is(err, ErrExpired) {
				t.Fatal("expired pending identity approved")
			}
			if manager.Snapshot().State != Expired || closes.Load() != 1 || manager.invite.secret != [32]byte{} {
				t.Fatal("expiry did not invalidate credentials and listener")
			}
		})
	}
}

func TestRealClockExpiryRetiresIdleListener(t *testing.T) {
	closed := make(chan struct{}, 1)
	manager, err := NewManager(syntheticKey(t, 1), Options{CloseInvitation: func() error { closed <- struct{}{}; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Create(syntheticEndpoint(), time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
		if manager.Snapshot().State != Expired {
			t.Fatal("listener retired without expiring invitation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("idle invitation listener did not expire automatically")
	}
}

func TestDenialBurnsInviteAndRestartInvalidatesIt(t *testing.T) {
	manager, invite := managerForTest(t, nil, nil)
	peer := syntheticKey(t, 2)
	_, _ = manager.Reserve(invite.Link(), peer)
	if err := manager.Revoke(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reserve(invite.Link(), peer); !errors.Is(err, ErrUnavailable) {
		t.Fatal("denied invite revived")
	}
	replacement, err := manager.Create(syntheticEndpoint(), 0)
	if err != nil || replacement.secret == invite.secret {
		t.Fatal("new invite reused old credential")
	}
	if _, err := manager.Reserve(invite.Link(), peer); !errors.Is(err, ErrInvalidInvite) {
		t.Fatal("old credential accepted by new listener")
	}
	_ = manager.Close()
	if _, err := manager.Reserve(replacement.Link(), peer); !errors.Is(err, ErrClosed) {
		t.Fatal("closed manager accepted an invite")
	}
	restarted, _ := NewManager(invite.Owner(), Options{})
	t.Cleanup(func() { _ = restarted.Close() })
	if _, err := restarted.Reserve(replacement.Link(), peer); !errors.Is(err, ErrUnavailable) {
		t.Fatal("new manager revived pre-restart invitation")
	}
}

func TestCloseFailureFailsApprovalClosed(t *testing.T) {
	manager, invite := managerForTest(t, nil, func() error { return errors.New("synthetic listener failure") })
	peer := syntheticKey(t, 2)
	_, _ = manager.Reserve(invite.Link(), peer)
	contact, err := manager.Approve(peer.Fingerprint())
	if contact != nil || !errors.Is(err, ErrListenerClose) {
		t.Fatal("contact activated despite live invitation listener")
	}
	if snapshot := manager.Snapshot(); snapshot.State != Revoked || snapshot.LifecycleError == "" {
		t.Fatal("listener failure not surfaced")
	}
	if _, err := manager.Create(syntheticEndpoint(), 0); !errors.Is(err, ErrListenerClose) {
		t.Fatal("new listener admitted after failed teardown")
	}
	if manager.invite.secret != [32]byte{} {
		t.Fatal("credential retained after failed listener close")
	}
}

func TestExpiryCloseFailureIsVisibleAndPreventsReuse(t *testing.T) {
	var elapsed atomic.Int64
	base := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	manager, _ := managerForTest(t, func() time.Time { return base.Add(time.Duration(elapsed.Load())) }, func() error { return errors.New("synthetic close error") })
	elapsed.Store(int64(DefaultLifetime))
	snapshot := manager.Snapshot()
	if snapshot.State != Expired || snapshot.LifecycleError != ErrListenerClose.Error() {
		t.Fatal("expired listener teardown failure not visible")
	}
	if _, err := manager.Create(syntheticEndpoint(), 0); !errors.Is(err, ErrListenerClose) {
		t.Fatal("failed listener teardown permitted new invitation")
	}
	if !errors.Is(manager.Close(), ErrListenerClose) {
		t.Fatal("shutdown swallowed listener teardown failure")
	}
}

func TestInviteParserRejectsMalformedAndAlteredLinks(t *testing.T) {
	manager, invite := managerForTest(t, nil, nil)
	link := invite.Link()
	parsed, err := ParseInvite(link)
	if err != nil || parsed.Link() != link || parsed.Owner() != invite.Owner() {
		t.Fatal("canonical invitation did not round trip")
	}
	mutate := func(key, value string) string {
		u, _ := url.Parse(link)
		query := u.Query()
		query.Set(key, value)
		u.RawQuery = query.Encode()
		return u.String()
	}
	malformed := []string{
		"", strings.Repeat("x", MaxLinkBytes+1), strings.Replace(link, "whatsup:", "https:", 1),
		strings.Replace(link, "//pair/", "//evil/", 1), strings.Replace(link, "/v1?", "/v2?", 1),
		link + "#secret", link + "&id=duplicate", link + "&unknown=field", link + "&bad=%xx",
		strings.Replace(link, "//pair/", "//user@pair/", 1), mutate("secret", "short"),
		mutate("owner", strings.Repeat("A", 43)), mutate("expires", "-1"), mutate("expires", "99999999999999999999"),
		mutate("endpoint", "../space invalid"), mutate("endpoint", strings.Repeat("x", MaxEndpointBytes+1)),
		mutate("endpoint", "\\\\host\\share"), mutate("endpoint", "scheme://user@host"), mutate("endpoint", "control\ncharacter"),
	}
	for index, bad := range malformed {
		if _, err := ParseInvite(bad); !errors.Is(err, ErrInvalidInvite) {
			t.Errorf("malformed link %d accepted", index)
		}
	}
	// These links remain syntactically valid but cannot change the server's
	// identity, address, expiry, ID, or secret binding.
	for _, altered := range []string{
		mutate("owner", syntheticKey(t, 3).String()), mutate("endpoint", syntheticEndpoint()),
		mutate("expires", fmt.Sprint(invite.expires.Add(time.Second).Unix())),
		mutate("id", base64.RawURLEncoding.EncodeToString(make([]byte, 16))),
		mutate("secret", base64.RawURLEncoding.EncodeToString(make([]byte, 32))),
	} {
		if _, err := manager.Reserve(altered, syntheticKey(t, 2)); !errors.Is(err, ErrInvalidInvite) {
			t.Fatal("altered invite changed server-side binding")
		}
	}
	if manager.Snapshot().State != Created {
		t.Fatal("invalid input consumed valid invitation")
	}
	encoded, _ := json.Marshal(invite)
	if string(encoded) != "{}" || fmt.Sprintf("%v", invite) != "[private WhatsUp invitation]" || fmt.Sprintf("%#v", invite) != "[private WhatsUp invitation]" {
		t.Fatal("invite leaked through ordinary formatting or JSON")
	}
}

func TestSelfPairAndUnboundedLifetimesRejected(t *testing.T) {
	manager, invite := managerForTest(t, nil, nil)
	for _, key := range []identity.PublicKey{invite.Owner(), {}} {
		if _, err := manager.Reserve(invite.Link(), key); !errors.Is(err, identity.ErrInvalidKey) {
			t.Fatal("invalid or local candidate accepted")
		}
	}
	if _, err := manager.Create(syntheticEndpoint(), 0); !errors.Is(err, ErrUnavailable) {
		t.Fatal("multiple simultaneous invitations created for one conversation")
	}
	_ = manager.Revoke()
	for _, lifetime := range []time.Duration{-time.Second, time.Nanosecond, 24 * time.Hour} {
		if _, err := manager.Create(syntheticEndpoint(), lifetime); !errors.Is(err, ErrInvalidInvite) {
			t.Fatal("invalid invitation lifetime accepted")
		}
	}
}

func FuzzParseInvite(f *testing.F) {
	f.Add("")
	f.Add("whatsup://pair/v1?secret=synthetic")
	f.Add(strings.Repeat("x", MaxLinkBytes+1))
	endpoint, err := address.Parse(syntheticEndpoint())
	if err != nil {
		f.Fatal(err)
	}
	f.Add((Invite{id: [16]byte{1}, secret: [32]byte{2}, owner: identity.PublicKey{3}, endpoint: endpoint, expires: time.Unix(1800000000, 0).UTC()}).Link())
	f.Fuzz(func(t *testing.T, link string) {
		invite, err := ParseInvite(link)
		if err == nil && (invite.Link() != link || !invite.Owner().Valid() || !invite.Endpoint().Valid()) {
			t.Fatal("parser accepted noncanonical invite")
		}
	})
}
