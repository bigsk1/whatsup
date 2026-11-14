package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"

	"whatsup/internal/identity"
	"whatsup/internal/pairing"
)

func testApproval(t *testing.T) *pairing.Contact {
	t.Helper()
	local, _ := identity.FromSeed(bytes.Repeat([]byte{1}, 32))
	peer, _ := identity.FromSeed(bytes.Repeat([]byte{2}, 32))
	manager, err := pairing.NewManager(local.PublicKey(), pairing.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	ci := tailcat.ConnInfo{
		ServerPublic:      tailcat.NodePublic{NodePublic: key.NewNode().Public()},
		ServerDiscoPublic: tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()},
		PresharedKey:      tailcat.NewPresharedKey(), RegionID: 1,
	}
	invite, err := manager.Create(string(ci.Addr()), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reserve(invite.Link(), peer.PublicKey()); err != nil {
		t.Fatal(err)
	}
	contact, err := manager.Approve(peer.PublicKey().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(contact.Revoke)
	return contact
}

func testCredentials(t *testing.T) *ContactCredentials {
	t.Helper()
	credentials, err := NewContactCredentials(testApproval(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(credentials.Revoke)
	return credentials
}

// Only tests synthesize this unexported record. No production protocol has
// authenticated reciprocal identities or bound these node keys yet.
func bindSyntheticCredentials(credentials *ContactCredentials, peer PeerKey) {
	s := credentials.state
	s.binding = &verifiedBinding{
		localIdentity: s.approval.Local(), peerIdentity: s.approval.Peer(),
		localNode: s.node.Public(), peerNode: peer.node,
	}
}

func testOptions(t *testing.T) ServerOptions {
	t.Helper()
	options := ServerOptions{
		Credentials: testCredentials(t), ApprovedPeer: testCredentials(t).PeerKey(),
		Policy: RelayFallback, AllowRelayFallback: true, DERPMapURL: "https://relay.example.test/derpmap.json",
	}
	bindSyntheticCredentials(options.Credentials, options.ApprovedPeer)
	return options
}

func TestUnavailablePolicyNeverConstructsTransport(t *testing.T) {
	for _, approved := range []bool{false, true} {
		options := testOptions(t)
		options.Policy, options.AllowRelayFallback = DirectOnly, approved
		if transport, err := NewContactServer(options); !errors.Is(err, ErrDirectOnlyUnavailable) || transport != nil {
			t.Fatal("direct-only transport must remain unavailable regardless of relay consent")
		}
	}
	options := testOptions(t)
	options.AllowRelayFallback = false
	if _, err := NewContactServer(options); !errors.Is(err, ErrRelayConsentRequired) {
		t.Fatal("relay fallback started without explicit consent")
	}
	options.Policy = Policy(255)
	if _, err := NewContactServer(options); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("unknown policy did not fail closed")
	}
	if _, err := NewContactServer(ServerOptions{}); !errors.Is(err, ErrDirectOnlyUnavailable) {
		t.Fatal("zero configuration did not fail closed")
	}
}

func TestCredentialsMustBeIndependentAndNonzero(t *testing.T) {
	one, two := testCredentials(t), testCredentials(t)
	if one.state.node.IsZero() || one.state.psk.IsZero() || one.state.node.Public() == two.state.node.Public() || one.state.psk.Equal(two.state.psk) {
		t.Fatal("contact credentials must be fresh and nonzero")
	}
	for _, credentials := range []*ContactCredentials{nil, {}, {state: &contactCredentialState{}}, {state: &contactCredentialState{node: key.NewNode()}}, {state: &contactCredentialState{psk: tailcat.NewPresharedKey()}}} {
		options := testOptions(t)
		options.Credentials = credentials
		if _, err := NewContactServer(options); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatal("missing node key or PSK was admitted")
		}
	}
	options := testOptions(t)
	options.ApprovedPeer = options.Credentials.PeerKey()
	if _, err := NewContactServer(options); !errors.Is(err, ErrInvalidPeer) {
		t.Fatal("self-peer was accepted")
	}
	options.ApprovedPeer = PeerKey{}
	if _, err := NewContactServer(options); !errors.Is(err, ErrInvalidPeer) {
		t.Fatal("zero peer was accepted")
	}
}

func TestBearerMaterialCannotLeakThroughOrdinaryFormattingOrJSON(t *testing.T) {
	credentials := testCredentials(t)
	address := ContactAddress{encoded: "synthetic-secret-address"}
	for _, value := range []any{credentials, *credentials, address} {
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("bearer material must not be JSON serializable")
		}
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if formatted := fmt.Sprintf(format, value); !strings.Contains(formatted, "[private contact") {
				t.Fatal("ordinary formatting did not redact bearer material")
			}
		}
	}
}

func TestContactCredentialsCannotBeReusedAcrossServers(t *testing.T) {
	options := testOptions(t)
	server, err := NewContactServer(options)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	copyOfCredentials := *options.Credentials
	options.Credentials = &copyOfCredentials
	options.ApprovedPeer = testCredentials(t).PeerKey()
	if _, err := NewContactServer(options); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("contact PSK was reused by a second server")
	}
}

func TestPeerKeysRequireCanonicalNonzeroPublicMaterial(t *testing.T) {
	valid := testCredentials(t).PeerKey()
	parsed, err := ParsePeerKey(valid.String())
	if err != nil || parsed != valid {
		t.Fatal("valid public key did not round trip")
	}
	for _, encoded := range []string{"", valid.String() + "\n", " " + valid.String(), "nodekey:" + strings.Repeat("0", 64), "nodekey:" + strings.Repeat("A", 64), "privkey:" + strings.Repeat("1", 64)} {
		if _, err := ParsePeerKey(encoded); !errors.Is(err, ErrInvalidPeer) {
			t.Fatal("invalid public key was accepted")
		}
	}
}

func TestRelayConfigurationCannotDisableTLSOrEmbedCredentials(t *testing.T) {
	for _, raw := range []string{"", "http://relay.example/map", "https://user:password@relay.example/map", "https://relay.example/map#fragment", "https:relay.example/map", "//relay.example/map", "https://"} {
		options := testOptions(t)
		options.DERPMapURL = raw
		if _, err := NewContactServer(options); !errors.Is(err, ErrInvalidRelay) {
			t.Fatal("unsafe relay map URL was accepted")
		}
	}
}

func TestAdmissionIsExactAndRevocationCannotReopen(t *testing.T) {
	options := testOptions(t)
	server, err := NewContactServer(options)
	if err != nil {
		t.Fatal(err)
	}
	if server.server.AllowClient == nil || !server.server.AllowClient(options.ApprovedPeer.node) {
		t.Fatal("approved peer was not admitted by explicit predicate")
	}
	if server.server.AllowClient(key.NodePublic{}) || server.server.AllowClient(key.NewNode().Public()) {
		t.Fatal("unknown peer admitted")
	}
	if server.server.DisablePresharedKey || server.server.PresharedKey.IsZero() || server.server.OnTCP != nil || server.server.OnTCPForward != nil || server.server.OnUDP != nil || server.server.OnUDPForward != nil || server.server.AllowProxy != nil {
		t.Fatal("adapter exposed another network service or disabled the PSK")
	}
	if _, err := server.AddressForApprovedPeer(); !errors.Is(err, ErrNotStarted) {
		t.Fatal("unstarted server exposed a transport address")
	}
	if _, err := server.Accept(); !errors.Is(err, ErrNotStarted) {
		t.Fatal("unstarted server accepted a stream")
	}
	if err := server.Revoke(); err != nil {
		t.Fatal(err)
	}
	if server.server.AllowClient(options.ApprovedPeer.node) {
		t.Fatal("revoked peer admitted again")
	}
	if err := server.Start(context.Background()); !errors.Is(err, net.ErrClosed) {
		t.Fatal("revoked transport was reopened")
	}
}

// These tests exercise the wrapper's lifecycle using net.Pipe and a fake
// authenticated identity lookup. They are NOT WireGuard/DERP integration evidence.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn, 2), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case conn := <-l.conns:
		return conn, nil
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func newLifecycleServer(t *testing.T, peer key.NodePublic, listener *pipeListener) *ContactServer {
	t.Helper()
	credentials := testCredentials(t)
	bindSyntheticCredentials(credentials, PeerKey{node: peer})
	return &ContactServer{
		listener: listener, peer: peer, started: true,
		peerKey:     func(net.Addr) (key.NodePublic, bool) { return peer, true },
		credentials: credentials,
	}
}

func TestAcceptedStreamIsRecheckedAndConnectionCountBounded(t *testing.T) {
	listener := newPipeListener()
	server := newLifecycleServer(t, key.NewNode().Public(), listener)
	defer server.Close()
	server.peerKey = func(net.Addr) (key.NodePublic, bool) { return key.NewNode().Public(), true }
	rejected, other := net.Pipe()
	defer other.Close()
	listener.conns <- rejected
	if _, err := server.Accept(); !errors.Is(err, ErrPeerRejected) {
		t.Fatal("unexpected authenticated peer accepted")
	}
	server.peerKey = func(net.Addr) (key.NodePublic, bool) { return server.peer, true }
	first, firstPeer := net.Pipe()
	defer firstPeer.Close()
	listener.conns <- first
	active, err := server.Accept()
	if err != nil {
		t.Fatal(err)
	}
	second, secondPeer := net.Pipe()
	defer secondPeer.Close()
	listener.conns <- second
	if _, err := server.Accept(); !errors.Is(err, ErrConnectionLimit) {
		t.Fatal("second concurrent stream exceeded connection limit")
	}
	if err := active.Close(); err != nil {
		t.Fatal(err)
	}
	third, thirdPeer := net.Pipe()
	defer thirdPeer.Close()
	listener.conns <- third
	if _, err := server.Accept(); err != nil {
		t.Fatal("closed stream did not release connection slot")
	}
}

func TestRevokeUnblocksAcceptedAndPendingConnections(t *testing.T) {
	listener := newPipeListener()
	server := newLifecycleServer(t, key.NewNode().Public(), listener)
	local, remote := net.Pipe()
	defer remote.Close()
	listener.conns <- local
	active, err := server.Accept()
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() { _, err := active.Read(make([]byte, 1)); readDone <- err }()
	acceptDone := make(chan error, 1)
	go func() { _, err := server.Accept(); acceptDone <- err }()
	if err := server.Revoke(); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan error{readDone, acceptDone} {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("revocation allowed a pending operation to succeed")
			}
		case <-time.After(time.Second):
			t.Fatal("revocation did not close pending operation")
		}
	}
	if _, err := server.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatal("revoked transport accepted another stream")
	}
}

func TestRevocationWinsConcurrentAccept(t *testing.T) {
	for range 50 {
		listener := newPipeListener()
		peer := key.NewNode().Public()
		server := newLifecycleServer(t, peer, listener)
		inLookup, continueLookup := make(chan struct{}), make(chan struct{})
		server.peerKey = func(net.Addr) (key.NodePublic, bool) {
			close(inLookup)
			<-continueLookup
			return peer, true
		}
		local, remote := net.Pipe()
		listener.conns <- local
		done := make(chan error, 1)
		go func() { _, err := server.Accept(); done <- err }()
		<-inLookup
		if err := server.Revoke(); err != nil {
			t.Fatal(err)
		}
		close(continueLookup)
		if err := <-done; !errors.Is(err, net.ErrClosed) {
			t.Fatal("connection escaped concurrent revocation")
		}
		_ = remote.Close()
	}
}
