package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"

	"whatsup/internal/identity"
	"whatsup/internal/storage"
)

func ongoingRecords(t *testing.T, profile *RelayProfile) (storage.PendingRecord, storage.PendingRecord) {
	t.Helper()
	owner, err := identity.Generate()
	if err != nil {
		t.Fatal("could not create disposable identity")
	}
	joiner, err := identity.Generate()
	if err != nil {
		t.Fatal("could not create disposable identity")
	}
	server := tailcat.NewPrivateKey()
	client := key.NewNode()
	privateServer, _ := server.Private.MarshalText()
	privateClient, _ := client.MarshalText()
	server.Public.RegionID = profile.state.defaultRegion
	left := storage.PendingRecord{Version: 2, ID: base64.RawURLEncoding.EncodeToString([]byte("synthetic-ID-001")), Role: "inviter",
		Local: owner.PublicKey(), Peer: joiner.PublicKey(), LocalNodePrivate: string(privateServer), LocalNode: server.Private.Public().String(), PeerNode: client.Public().String(),
		PresharedKey: base64.RawURLEncoding.EncodeToString(server.Public.PresharedKey[:]), RelayServiceURL: profile.state.source, RelayFallbackConsent: true, ServerEndpoint: string(server.Public.Addr())}
	right := left
	right.Role, right.Local, right.Peer = "joiner", left.Peer, left.Local
	right.LocalNodePrivate, right.LocalNode, right.PeerNode = string(privateClient), left.PeerNode, left.LocalNode
	clear(privateServer)
	clear(privateClient)
	if storage.ValidatePendingRecord(left) != nil || storage.ValidatePendingRecord(right) != nil {
		t.Fatal("invalid disposable ongoing records")
	}
	return left, right
}

func TestOngoingRejectsPolicyAndSubstitutionBeforeNetwork(t *testing.T) {
	profile := reviewedTestProfile(t)
	owner, joiner := ongoingRecords(t, profile)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if conn, err := DialOngoing(ctx, joiner, profile, RelayFallback, true); conn != nil || err == nil {
		t.Fatal("canceled dial constructed a connection")
	}
	if listener, err := NewOngoingListener(nil, OngoingOptions{}); listener != nil || err == nil {
		t.Fatal("nil listener context accepted")
	}
	for _, consent := range []bool{false, true} {
		if listener, err := NewOngoingListener(context.Background(), OngoingOptions{Record: owner, Profile: profile, Policy: DirectOnly, AllowRelayFallback: consent}); listener != nil || !errors.Is(err, ErrDirectOnlyUnavailable) {
			t.Fatal("ongoing listener silently weakened direct-only")
		}
	}
	mutations := []func(*storage.PendingRecord){
		func(r *storage.PendingRecord) { r.Version = 1 },
		func(r *storage.PendingRecord) { r.RelayServiceURL = "https://different.example/map" },
		func(r *storage.PendingRecord) { r.RelayFallbackConsent = false },
		func(r *storage.PendingRecord) { r.ServerEndpoint = "invalid" },
		func(r *storage.PendingRecord) {
			ci, _ := tailcat.ParseAddr(tailcat.Addr(r.ServerEndpoint))
			ci.RegionID++
			r.ServerEndpoint = string(ci.Addr())
		},
		func(r *storage.PendingRecord) {
			ci, _ := tailcat.ParseAddr(tailcat.Addr(r.ServerEndpoint))
			ci.PresharedKey = tailcat.NewPresharedKey()
			r.ServerEndpoint = string(ci.Addr())
		},
		func(r *storage.PendingRecord) {
			ci, _ := tailcat.ParseAddr(tailcat.Addr(r.ServerEndpoint))
			ci.ServerPublic = tailcat.NodePublic{NodePublic: key.NewNode().Public()}
			r.ServerEndpoint = string(ci.Addr())
		},
		func(r *storage.PendingRecord) {
			ci, _ := tailcat.ParseAddr(tailcat.Addr(r.ServerEndpoint))
			ci.ServerDiscoPublic = tailcat.DiscoPublic{DiscoPublic: key.NewDisco().Public()}
			r.ServerEndpoint = string(ci.Addr())
		},
	}
	for _, mutate := range mutations {
		record := owner
		mutate(&record)
		if listener, err := NewOngoingListener(context.Background(), OngoingOptions{Record: record, Profile: profile, Policy: RelayFallback, AllowRelayFallback: true}); listener != nil || err == nil {
			t.Fatal("unapproved record or relay substitution reached networking")
		}
	}
	if listener, err := NewOngoingListener(context.Background(), OngoingOptions{Record: joiner, Profile: profile, Policy: RelayFallback, AllowRelayFallback: true}); listener != nil || err == nil {
		t.Fatal("joiner could assume inviter listener role")
	}
}

func TestOngoingFormattingAndZeroValuesDoNotExposeCredentials(t *testing.T) {
	var listener OngoingListener
	for _, item := range []any{listener, &listener} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if fmt.Sprintf(format, item) != "[private WhatsUp contact listener]" {
				t.Fatal("listener formatting exposed private state")
			}
		}
		if _, err := json.Marshal(item); err == nil {
			t.Fatal("listener serialized into a DTO")
		}
	}
	if _, err := listener.AddrEndpoint(); err == nil {
		t.Fatal("zero listener returned an address")
	}
	if _, err := listener.Accept(context.Background()); err == nil {
		t.Fatal("zero listener accepted a stream")
	}
	if listener.Close() != nil {
		t.Fatal("zero listener cleanup failed")
	}
}

func ongoingConnectionPair(t *testing.T, ctx context.Context, listener *OngoingListener, record storage.PendingRecord, profile *RelayProfile) (net.Conn, net.Conn) {
	t.Helper()
	type accepted struct {
		conn net.Conn
		err  error
	}
	ready := make(chan accepted, 1)
	go func() { conn, err := listener.Accept(ctx); ready <- accepted{conn, err} }()
	client, err := DialOngoing(ctx, record, profile, RelayFallback, true)
	if err != nil {
		t.Fatal("ongoing dial failed")
	}
	t.Cleanup(func() { _ = client.Close() })
	select {
	case result := <-ready:
		if result.err != nil {
			t.Fatal("ongoing accept failed")
		}
		t.Cleanup(func() { _ = result.conn.Close() })
		return result.conn, client
	case <-ctx.Done():
		t.Fatal("ongoing stream did not connect")
		return nil, nil
	}
}

func requireOngoingBytes(t *testing.T, sender, receiver net.Conn) {
	t.Helper()
	message := []byte("synthetic ongoing transport payload")
	written := make(chan error, 1)
	go func() { _, err := sender.Write(message); written <- err }()
	_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
	actual := make([]byte, len(message))
	if _, err := io.ReadFull(receiver, actual); err != nil || !bytes.Equal(actual, message) || <-written != nil {
		t.Fatal("ongoing stream did not carry bounded synthetic bytes")
	}
	_ = receiver.SetReadDeadline(time.Time{})
}

func TestRealOngoingPreservesKeysAndEndpointAcrossReconnectAndRestart(t *testing.T) {
	profile := loopbackRelayProfile(t)
	owner, joiner := ongoingRecords(t, profile)
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	options := OngoingOptions{Record: owner, Profile: profile, Policy: RelayFallback, AllowRelayFallback: true}
	listener, err := NewOngoingListener(ctx, options)
	if err != nil {
		t.Fatal("could not start ongoing listener")
	}
	t.Cleanup(func() { _ = listener.Close() })
	if !listener.admit(mustPeerNode(t, joiner.LocalNode)) || listener.admit(key.NewNode().Public()) {
		t.Fatal("ongoing admission did not enforce exact stored peer")
	}
	endpoint, err := listener.AddrEndpoint()
	if err != nil || string(endpoint.EncodeForTransport()) != owner.ServerEndpoint {
		t.Fatal("ongoing listener changed the stored endpoint")
	}
	if listener.state.server.DisablePresharedKey || !listener.state.server.DisableDERPApplicationStats || listener.state.server.OnTCP != nil || listener.state.server.OnUDP != nil || listener.state.server.OnTCPForward != nil || listener.state.server.OnUDPForward != nil || listener.state.server.AllowProxy != nil {
		t.Fatal("ongoing listener enabled an unapproved service or weakened privacy")
	}
	for attempt := range 2 {
		server, client := ongoingConnectionPair(t, ctx, listener, joiner, profile)
		requireOngoingBytes(t, client, server)
		requireOngoingBytes(t, server, client)
		if attempt == 0 {
			probe, stop := context.WithTimeout(ctx, time.Second)
			extra, err := client.(*ongoingClientConn).client.DialTCPPort(probe, ChatPort+1)
			stop()
			if err == nil {
				_ = extra.Close()
				t.Fatal("approved node reached an additional tunnel service")
			}
		}
		if _, err := listener.Accept(ctx); !errors.Is(err, ErrConnectionLimit) {
			t.Fatal("one contact acquired a second simultaneous stream")
		}
		_ = server.Close()
		_ = client.Close()
	}
	_ = listener.Close()
	// A later map from the same selected operator may change its preferred
	// default. The saved contact must still use the agreed server region.
	updated := &RelayProfile{state: &relayProfileState{source: profile.state.source, expires: profile.state.expires, defaultRegion: 899,
		regions: make(map[tailcfg.DERPRegionID]*tailcfg.DERPRegion), wire: make(map[tailcfg.DERPRegionID]*tailcfg.DERPRegion)}}
	for id, region := range profile.state.regions {
		updated.state.regions[id] = cloneRelayRegion(region)
		updated.state.wire[id] = cloneRelayRegion(profile.state.wire[id])
	}
	alternate := cloneRelayRegion(profile.state.regions[900])
	alternate.RegionID = 899
	for _, node := range alternate.Nodes {
		node.RegionID = 899
		node.Name = "alternate-disposable-node"
	}
	updated.state.regions[899], updated.state.wire[899] = alternate, cloneRelayRegion(alternate)
	options.Profile = updated
	restarted, err := NewOngoingListener(ctx, options)
	if err != nil {
		t.Fatal("ongoing server failed to restart from same stored bundle")
	}
	defer restarted.Close()
	endpoint, err = restarted.AddrEndpoint()
	if err != nil || string(endpoint.EncodeForTransport()) != owner.ServerEndpoint || restarted.state.server.Key.Public().String() != owner.LocalNode || restarted.state.server.Region.RegionID != 900 {
		t.Fatal("restart replaced node identity, PSK or discovery endpoint")
	}
	server, client := ongoingConnectionPair(t, ctx, restarted, joiner, updated)
	requireOngoingBytes(t, client, server)
	cancel()
	if _, err := restarted.AddrEndpoint(); err == nil {
		// Context cancellation's callback can be scheduled just after Err;
		// the method independently checks the lifetime before publication.
		t.Fatal("canceled ongoing lifetime retained its endpoint")
	}
	_ = server.Close()
	_ = client.Close()
}

func mustPeerNode(t *testing.T, encoded string) key.NodePublic {
	t.Helper()
	var node key.NodePublic
	if node.UnmarshalText([]byte(encoded)) != nil || node.IsZero() {
		t.Fatal("invalid disposable node")
	}
	return node
}

func TestRealOngoingRejectsThirdNodeAndCancelRetiresAccept(t *testing.T) {
	profile := loopbackRelayProfile(t)
	owner, joiner := ongoingRecords(t, profile)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	listener, err := NewOngoingListener(ctx, OngoingOptions{Record: owner, Profile: profile, Policy: RelayFallback, AllowRelayFallback: true})
	if err != nil {
		t.Fatal("could not start disposable ongoing listener")
	}
	defer listener.Close()
	rogue := joiner
	private := key.NewNode()
	encoded, _ := private.MarshalText()
	rogue.LocalNodePrivate, rogue.LocalNode = string(encoded), private.Public().String()
	clear(encoded)
	denied, stop := context.WithTimeout(ctx, time.Second)
	conn, err := DialOngoing(denied, rogue, profile, RelayFallback, true)
	stop()
	if conn != nil || err == nil {
		t.Fatal("third node connected using the known contact PSK")
	}
	pending, stopAccept := context.WithCancel(ctx)
	finished := make(chan error, 1)
	go func() { _, err := listener.Accept(pending); finished <- err }()
	ready := time.NewTicker(time.Millisecond)
	defer ready.Stop()
	for {
		listener.state.mu.Lock()
		accepting := listener.state.accepting
		listener.state.mu.Unlock()
		if accepting {
			break
		}
		select {
		case <-ready.C:
		case <-ctx.Done():
			t.Fatal("ongoing accept did not enter its wait")
		}
	}
	stopAccept()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("canceled ongoing accept succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("ongoing accept ignored cancellation")
	}
	if _, err := listener.AddrEndpoint(); err == nil {
		t.Fatal("canceled pending accept retained its listener")
	}
}
