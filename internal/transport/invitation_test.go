package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"tailscale.com/derp/derpserver"
	"tailscale.com/envknob"
	"tailscale.com/feature"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"

	"whatsup/internal/transport/address"
)

func TestInvitationRejectsMissingPolicyBeforeStartingNetwork(t *testing.T) {
	profile := reviewedTestProfile(t)
	for _, options := range []InvitationOptions{
		{},
		{Profile: profile, Policy: DirectOnly, AllowRelayFallback: true},
		{Profile: profile, Policy: RelayFallback},
		{Policy: RelayFallback, AllowRelayFallback: true},
	} {
		if listener, err := NewInvitationListener(context.Background(), options); err == nil || listener != nil {
			t.Fatal("unsafe invitation options started a listener")
		}
	}
	if listener, err := NewInvitationListener(nil, InvitationOptions{Profile: profile, Policy: RelayFallback, AllowRelayFallback: true}); err == nil || listener != nil {
		t.Fatal("missing lifetime accepted")
	}
	for _, policy := range []Policy{DirectOnly, RelayFallback} {
		if conn, err := DialInvitation(context.Background(), address.Endpoint{}, profile, policy, false); err == nil || conn != nil {
			t.Fatal("dialer used unapproved relay fallback")
		}
	}
	if conn, err := DialInvitation(context.Background(), syntheticInvitationEndpoint(t, nil, 901), profile, RelayFallback, true); err == nil || conn != nil {
		t.Fatal("unknown peer region reached a dialer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if listener, err := NewInvitationListener(ctx, InvitationOptions{Profile: profile, Policy: RelayFallback, AllowRelayFallback: true}); err == nil || listener != nil {
		t.Fatal("canceled invitation startup succeeded")
	}
}

func TestInvitationBuildRequiresReviewedNetworkPrivacyTags(t *testing.T) {
	options := InvitationOptions{Profile: reviewedTestProfile(t), Policy: RelayFallback, AllowRelayFallback: true}
	if !networkPrivacyReady || feature.HookProxyFromEnvironment.GetOrNil() != nil || buildfeatures.HasPortMapper || buildfeatures.HasCaptivePortal {
		if err := invitationOptions(options); err == nil {
			t.Fatal("custom build enabled network transport without every reviewed omission tag")
		}
		return
	}
	if err := invitationOptions(options); err != nil {
		t.Fatal("reviewed network privacy build rejected valid approved options")
	}
}

func TestInvitationServerConfigurationHasOnlyFixedPortAndPrivacyControls(t *testing.T) {
	profile := reviewedTestProfile(t)
	server, err := newInvitationServer(profile, func(key.NodePublic) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if server.Key.IsZero() || server.PresharedKey.IsZero() || server.DisablePresharedKey || !server.DisableDERPApplicationStats || server.AllowClient == nil || server.Logf == nil || server.Region == nil || len(server.ServedTCPPorts) != 0 || len(server.ServedUDPPorts) != 0 || server.OnTCP != nil || server.OnTCPForward != nil || server.OnUDP != nil || server.OnUDPForward != nil || server.AllowProxy != nil {
		t.Fatal("invitation transport exposed another service or weakened privacy/encryption")
	}
	server.Region.Nodes[0].HostName = "mutated.example"
	if profile.state.regions[900].Nodes[0].HostName != "relay.example" {
		t.Fatal("runtime could mutate the operator profile")
	}
	if _, err := newInvitationServer(nil, func(key.NodePublic) bool { return true }); err == nil {
		t.Fatal("missing profile selected a public map")
	}
	if _, err := newInvitationServer(profile, nil); err == nil {
		t.Fatal("fail-open admission hook accepted")
	}
}

func TestInvitationAdmissionIsBoundedAndPinsOnlyAcceptedPeer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := &invitationState{life: ctx, peers: make(map[key.NodePublic]bool)}
	listener := &InvitationListener{state: state}
	if listener.admit(key.NodePublic{}) {
		t.Fatal("zero peer admitted")
	}
	var first key.NodePublic
	for i := range maxInvitationPeers {
		peer := key.NewNode().Public()
		if i == 0 {
			first = peer
		}
		if !listener.admit(peer) {
			t.Fatal("bounded invitation peer was rejected")
		}
	}
	if listener.admit(key.NewNode().Public()) {
		t.Fatal("unbounded invitation identities admitted")
	}
	state.accepted, state.peer = true, first
	if !listener.admit(first) || listener.admit(key.NewNode().Public()) {
		t.Fatal("accepted invitation stream did not pin its WireGuard peer")
	}
	state.closed = true
	if listener.admit(first) {
		t.Fatal("closed invitation retained admission")
	}
}

func TestInvitationFormattingAndZeroValuesAreClosed(t *testing.T) {
	for _, item := range []any{InvitationListener{}, &InvitationListener{}} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if fmt.Sprintf(format, item) != "[private WhatsUp invitation transport]" {
				t.Fatal("invitation transport formatting exposed state")
			}
		}
		if _, err := json.Marshal(item); err == nil {
			t.Fatal("invitation transport serialized into a bridge DTO")
		}
	}
	var empty *InvitationListener
	if _, err := empty.AddrEndpoint(); err == nil {
		t.Fatal("missing listener returned an endpoint")
	}
	if _, err := empty.Accept(context.Background()); err == nil {
		t.Fatal("missing listener accepted a connection")
	}
	if err := empty.Close(); err != nil {
		t.Fatal("zero listener cleanup failed")
	}
}

// loopbackRelayProfile is deliberately test-only. Production profiles are
// created only by FetchRelayProfile, which rejects every loopback/private IP and
// certificate override. This fixture uses an exact TLS certificate fingerprint,
// not disabled TLS verification, and never deploys or contacts a public relay.
func loopbackRelayProfile(t *testing.T) *RelayProfile {
	t.Helper()
	if !networkPrivacyReady || feature.HookProxyFromEnvironment.GetOrNil() != nil || buildfeatures.HasPortMapper || buildfeatures.HasCaptivePortal {
		t.Skip("real Tailcat check requires the reviewed proxy/portmapper/captive-portal omission tags")
	}
	envknob.SetenvForTest(t, "IN_TS_TEST", "true")
	relay := derpserver.New(key.NewNode(), func(string, ...any) {})
	server := httptest.NewUnstartedServer(derpserver.Handler(relay))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	t.Cleanup(func() { _ = relay.Close(); server.Close() })
	_, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	certificate := sha256.Sum256(server.TLS.Certificates[0].Certificate[0])
	region := &tailcfg.DERPRegion{RegionID: 900, RegionCode: "loopback", RegionName: "Disposable test relay", Nodes: []*tailcfg.DERPNode{{
		Name: "synthetic-local", RegionID: 900, HostName: "127.0.0.1", IPv4: "127.0.0.1", IPv6: "none", STUNPort: -1, DERPPort: number,
		CertName: "sha256-raw:" + hex.EncodeToString(certificate[:]),
	}}}
	return &RelayProfile{state: &relayProfileState{source: "https://operator.example/test-only", expires: time.Now().Add(time.Minute), defaultRegion: 900,
		regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{900: region}, wire: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{900: cloneRelayRegion(region)}}}
}

func TestRealInvitationWireGuardOverDisposableTLSRelay(t *testing.T) {
	profile := loopbackRelayProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	listener, err := NewInvitationListener(ctx, InvitationOptions{Profile: profile, Policy: RelayFallback, AllowRelayFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	endpoint, err := listener.AddrEndpoint()
	if err != nil || !endpoint.Valid() {
		t.Fatal("invitation endpoint unavailable")
	}
	type result struct {
		conn net.Conn
		err  error
	}
	accepted := make(chan result, 1)
	go func() { conn, err := listener.Accept(ctx); accepted <- result{conn, err} }()
	clientLife, cancelClient := context.WithCancel(ctx)
	defer cancelClient()
	client, err := DialInvitation(clientLife, endpoint, profile, RelayFallback, true)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var server net.Conn
	select {
	case got := <-accepted:
		if got.err != nil {
			t.Fatal(got.err)
		}
		server = got.conn
	case <-ctx.Done():
		t.Fatal("real invitation stream was not accepted")
	}
	defer server.Close()
	if _, err := listener.Accept(ctx); !errors.Is(err, ErrConnectionLimit) {
		t.Fatal("one invitation admitted a second application stream")
	}
	payload := []byte("synthetic private invitation transport")
	written := make(chan error, 1)
	go func() { _, err := client.Write(payload); written <- err }()
	actual := make([]byte, len(payload))
	_ = server.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(server, actual); err != nil || !bytes.Equal(payload, actual) || <-written != nil {
		t.Fatal("real encrypted invitation stream did not carry the bounded test payload")
	}
	reply := []byte("synthetic reply")
	go func() { _, err := server.Write(reply); written <- err }()
	actual = make([]byte, len(reply))
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(client, actual); err != nil || !bytes.Equal(reply, actual) || <-written != nil {
		t.Fatal("real encrypted invitation stream did not carry the bounded reverse payload")
	}
	// Cancellation must retire the owning Tailcat client, rather than leaving a
	// background transport alive after the application has abandoned pairing.
	cancelClient()
	readClosed := make(chan error, 1)
	go func() { _, err := client.Read(make([]byte, 1)); readClosed <- err }()
	select {
	case err := <-readClosed:
		if err == nil {
			t.Fatal("canceled invitation client remained readable")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled invitation client did not close promptly")
	}
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() { defer group.Done(); _ = listener.Close() }()
	}
	group.Wait()
	if _, err := listener.AddrEndpoint(); err == nil {
		t.Fatal("retired invitation retained endpoint credentials")
	}
	if listener.admit(key.NewNode().Public()) {
		t.Fatal("retired invitation allowed another peer")
	}
	if _, err := server.Write([]byte("closed")); err == nil {
		t.Fatal("retired invitation left the accepted stream open")
	}
}

func TestRealInvitationCancellationClosesPendingAccept(t *testing.T) {
	profile := loopbackRelayProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	listener, err := NewInvitationListener(ctx, InvitationOptions{Profile: profile, Policy: RelayFallback, AllowRelayFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	acceptLife, cancelAccept := context.WithCancel(ctx)
	accepted := make(chan error, 1)
	go func() { _, err := listener.Accept(acceptLife); accepted <- err }()
	cancelAccept()
	select {
	case err := <-accepted:
		if err == nil {
			t.Fatal("canceled invitation accept succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled invitation accept left a listener behind")
	}
	if _, err := listener.AddrEndpoint(); err == nil {
		t.Fatal("canceled invitation accept retained its ephemeral endpoint")
	}
}

func realRetirementPair(t *testing.T) (*InvitationListener, net.Conn, net.Conn, context.Context) {
	t.Helper()
	profile := loopbackRelayProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	t.Cleanup(cancel)
	listener, err := NewInvitationListener(ctx, InvitationOptions{Profile: profile, Policy: RelayFallback, AllowRelayFallback: true})
	if err != nil {
		t.Fatal("could not create disposable retirement listener")
	}
	t.Cleanup(func() { _ = listener.Close() })
	endpoint, err := listener.AddrEndpoint()
	if err != nil {
		t.Fatal("could not obtain retirement endpoint")
	}
	type accepted struct {
		conn net.Conn
		err  error
	}
	ready := make(chan accepted, 1)
	go func() { raw, err := listener.Accept(ctx); ready <- accepted{raw, err} }()
	client, err := DialInvitation(ctx, endpoint, profile, RelayFallback, true)
	if err != nil {
		t.Fatal("could not dial disposable retirement stream")
	}
	t.Cleanup(func() { _ = client.Close() })
	select {
	case result := <-ready:
		if result.err != nil {
			t.Fatal("could not accept disposable retirement stream")
		}
		t.Cleanup(func() { _ = result.conn.Close() })
		return listener, client, result.conn, ctx
	case <-ctx.Done():
		t.Fatal("disposable retirement stream was not accepted")
		return nil, nil, nil, nil
	}
}

func TestRealInvitationGracefulRetirementDrainsBothEngines(t *testing.T) {
	listener, client, server, ctx := realRetirementPair(t)
	active, ok := client.(InvitationRetirer)
	if !ok {
		t.Fatal("client did not expose typed invitation retirement")
	}
	passive, ok := server.(InvitationRetirer)
	if !ok {
		t.Fatal("server did not expose typed invitation retirement")
	}
	finished := make(chan error, 1)
	go func() { finished <- active.RetireInvitation(ctx, false) }()
	var unexpected [1]byte
	_ = server.SetReadDeadline(time.Now().Add(invitationDrainTimeout))
	if n, err := server.Read(unexpected[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatal("active retirement did not transmit its TCP EOF")
	}
	if err := passive.RetireInvitation(ctx, true); err != nil {
		t.Fatal("passive retirement did not drain acknowledged TCP traffic")
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal("active retirement did not drain acknowledged TCP traffic")
		}
	case <-ctx.Done():
		t.Fatal("active retirement exceeded its bounded lifetime")
	}
	if err := active.RetireInvitation(ctx, false); err != nil {
		t.Fatal("completed active retirement was not idempotent")
	}
	if err := passive.RetireInvitation(ctx, true); err != nil {
		t.Fatal("completed passive retirement was not idempotent")
	}
	if _, err := listener.AddrEndpoint(); err == nil {
		t.Fatal("drained invitation retained published credentials")
	}
	ownedClient := client.(*invitationClientConn)
	if !ownedClient.client.Key.IsZero() || ownedClient.client.Server != "" {
		t.Fatal("drained client retained ephemeral credentials")
	}
}

func TestRealInvitationRetirementCanBeCanceledOrImmediatelyAborted(t *testing.T) {
	for _, mode := range []string{"cancel", "abort"} {
		t.Run(mode, func(t *testing.T) {
			_, client, server, ctx := realRetirementPair(t)
			work, cancel := context.WithCancel(ctx)
			defer cancel()
			finished := make(chan error, 1)
			go func() { finished <- client.(InvitationRetirer).RetireInvitation(work, false) }()
			var unexpected [1]byte
			_ = server.SetReadDeadline(time.Now().Add(invitationDrainTimeout))
			if n, err := server.Read(unexpected[:]); n != 0 || !errors.Is(err, io.EOF) {
				t.Fatal("active retirement did not enter its bounded half-close")
			}
			if mode == "cancel" {
				cancel()
			} else {
				if err := client.Close(); err != nil {
					t.Fatal("immediate abort could not interrupt retirement")
				}
			}
			select {
			case err := <-finished:
				if err == nil {
					t.Fatal("aborted retirement was reported as graceful")
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation or abort waited for the drain deadline")
			}
		})
	}
}

func TestRealInvitationPassiveRetirementRequiresObservedEOF(t *testing.T) {
	listener, _, server, ctx := realRetirementPair(t)
	if err := server.(InvitationRetirer).RetireInvitation(ctx, true); err == nil {
		t.Fatal("server retired passively without observing the peer TCP EOF")
	}
	if _, err := listener.AddrEndpoint(); err == nil {
		t.Fatal("invalid graceful retirement left invitation credentials live")
	}
}
