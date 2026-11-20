package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"

	"whatsup/internal/transport/address"
)

// callMediaProbePort is the tunnel UDP port proposed for call media. Nothing in
// the application serves it yet; this test pins the Tailcat behaviour a
// call-scoped media flow would depend on, so a dependency update that changes
// it fails here first.
const callMediaProbePort = 48732

func udpProbeServer(t *testing.T, ctx context.Context, profile *RelayProfile, admit func(key.NodePublic) bool) *tailcat.Server {
	t.Helper()
	var psk tailcat.PresharedKey
	if _, err := rand.Read(psk[:]); err != nil {
		t.Fatal(err)
	}
	server := &tailcat.Server{
		Key: key.NewNode(), PresharedKey: psk,
		Region:                      cloneRelayRegion(profile.state.regions[profile.state.defaultRegion]),
		DERPMapURL:                  profile.state.source,
		DisableDERPApplicationStats: true,
		AllowClient:                 admit,
		// Same empty allowlists as the application: only a live listener opens a port.
		ServedTCPPorts: []filter.PortRange{}, ServedUDPPorts: []filter.PortRange{},
		Logf: func(string, ...any) {},
	}
	if err := server.StartContext(ctx); err != nil {
		t.Fatal("disposable Tailcat server did not start")
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func udpProbeClient(t *testing.T, profile *RelayProfile, server *tailcat.Server, private key.NodePrivate) *tailcat.Client {
	t.Helper()
	// Same compact region reference the application publishes: the dialer
	// resolves the region from its own selected profile, never from the peer.
	info, err := tailcat.ParseAddr(server.TailcatAddr())
	if err != nil {
		t.Fatal("server address could not be parsed")
	}
	info.Region, info.RegionID = nil, profile.state.defaultRegion
	endpoint, err := address.Parse(string(info.Addr()))
	if err != nil {
		t.Fatal("server address is not a canonical endpoint")
	}
	trusted, err := profile.trustedEndpoint(endpoint)
	if err != nil {
		t.Fatal("server endpoint is not inside the selected relay profile")
	}
	client := &tailcat.Client{Server: trusted, Key: private, Logf: func(string, ...any) {},
		DERPMapURL: profile.state.source, DisableDERPApplicationStats: true}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// acceptFlows runs the listener's single Accept loop. A timed-out wait must not
// leave a second Accept behind, because it would swallow the next real flow.
func acceptFlows(listener net.Listener) <-chan net.Conn {
	flows := make(chan net.Conn, 8)
	go func() {
		defer close(flows)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			flows <- conn
		}
	}()
	return flows
}

// flowWithin reports the next accepted UDP flow, or nil when none arrives in time.
func flowWithin(flows <-chan net.Conn, wait time.Duration) net.Conn {
	select {
	case conn := <-flows:
		return conn
	case <-time.After(wait):
		return nil
	}
}

func readDatagram(t *testing.T, conn net.Conn, wait time.Duration) ([]byte, bool) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2048)
	n, err := conn.Read(buffer)
	if err != nil {
		return nil, false
	}
	return buffer[:n], true
}

func TestTailcatUDPFlowIsExactPeerDatagramAndClosedWithoutListener(t *testing.T) {
	profile := loopbackRelayProfile(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	approved := key.NewNode()
	server := udpProbeServer(t, ctx, profile, func(peer key.NodePublic) bool { return peer == approved.Public() })
	client := udpProbeClient(t, profile, server, approved)
	listenAddress := ":" + strconv.Itoa(callMediaProbePort)

	// 1. No listener: the port is closed. Datagrams sent now must not be queued
	// for a listener that opens later.
	// Bring the tunnel up first, so these datagrams really reach the server's
	// packet filter and are not merely lost during the WireGuard handshake.
	if _, err := client.Ping(ctx); err != nil {
		t.Fatal("approved client could not reach the server through the tunnel")
	}
	early, err := client.DialUDPPort(ctx, callMediaProbePort)
	if err != nil {
		t.Fatal("approved client could not open a tunnel UDP socket")
	}
	for i := 0; i < 5; i++ {
		if _, err := early.Write([]byte("sent-before-any-listener")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	_ = early.Close()
	time.Sleep(300 * time.Millisecond)

	listener, err := server.Listen(ctx, "udp", listenAddress)
	if err != nil {
		t.Fatal("server could not listen on the tunnel UDP port")
	}
	flows := acceptFlows(listener)
	if stale := flowWithin(flows, 1200*time.Millisecond); stale != nil {
		_ = stale.Close()
		t.Fatal("datagrams sent while the port had no listener were delivered later")
	}

	// 2. With a listener: one flow from the approved peer, datagram boundaries
	// preserved up to the documented ceiling, in both directions.
	flow, err := client.DialUDPPort(ctx, callMediaProbePort)
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	sizes := []int{1, 80, 1150, tailcat.MaxUDPPayload}
	first := bytes.Repeat([]byte{0xA5}, sizes[0])
	// The first datagram also brings the tunnel up, so resend until the flow appears.
	var inbound net.Conn
	for deadline := time.Now().Add(15 * time.Second); inbound == nil; {
		if time.Now().After(deadline) {
			t.Fatal("no UDP flow reached the listener in time")
		}
		if _, err := flow.Write(first); err != nil {
			t.Fatal(err)
		}
		inbound = flowWithin(flows, 250*time.Millisecond)
	}
	defer inbound.Close()
	if _, ok := inbound.(tailcat.ConnPacketConn); !ok {
		t.Fatal("accepted UDP flow is not a datagram connection")
	}
	peer, known := server.PeerKey(inbound.RemoteAddr())
	if !known || peer != approved.Public() {
		t.Fatal("accepted UDP flow is not attributed to the approved WireGuard peer")
	}
	// Drain the bring-up copies of the first datagram.
	for {
		if _, ok := readDatagram(t, inbound, 300*time.Millisecond); !ok {
			break
		}
	}
	for _, size := range sizes {
		payload := make([]byte, size)
		if _, err := rand.Read(payload); err != nil {
			t.Fatal(err)
		}
		if _, err := flow.Write(payload); err != nil {
			t.Fatalf("client could not send a %d-byte datagram: %v", size, err)
		}
		got, ok := readDatagram(t, inbound, 3*time.Second)
		if !ok || !bytes.Equal(got, payload) {
			t.Fatalf("server did not receive the exact %d-byte datagram (got %d bytes)", size, len(got))
		}
		if _, err := inbound.Write(payload); err != nil {
			t.Fatalf("server could not reply with a %d-byte datagram: %v", size, err)
		}
		back, ok := readDatagram(t, flow, 3*time.Second)
		if !ok || !bytes.Equal(back, payload) {
			t.Fatalf("client did not receive the exact %d-byte reply (got %d bytes)", size, len(back))
		}
	}

	// 3. Call-rate traffic: 20 ms cadence, both directions, nothing lost or
	// reordered. Both nodes share one host, so the tunnel may run direct or
	// through the loopback relay; timing is logged, not asserted.
	const frames = 150
	var worst, total time.Duration
	for i := 0; i < frames; i++ {
		payload := make([]byte, 96)
		payload[0], payload[1] = byte(i), byte(i>>8)
		sent := time.Now()
		if _, err := flow.Write(payload); err != nil {
			t.Fatal(err)
		}
		got, ok := readDatagram(t, inbound, 2*time.Second)
		if !ok || got[0] != payload[0] || got[1] != payload[1] {
			t.Fatalf("datagram %d was lost or reordered", i)
		}
		if _, err := inbound.Write(got); err != nil {
			t.Fatal(err)
		}
		if back, ok := readDatagram(t, flow, 2*time.Second); !ok || back[0] != payload[0] || back[1] != payload[1] {
			t.Fatalf("reply %d was lost or reordered", i)
		}
		elapsed := time.Since(sent)
		total += elapsed
		if elapsed > worst {
			worst = elapsed
		}
		time.Sleep(20*time.Millisecond - min(elapsed, 20*time.Millisecond))
	}
	t.Logf("%d round trips through the tunnel on one host: mean %s, worst %s", frames, total/frames, worst)

	// 4. A peer the server did not approve gets nothing, even with the listener open.
	stranger := udpProbeClient(t, profile, server, key.NewNode())
	strangerLife, stopStranger := context.WithTimeout(ctx, 4*time.Second)
	defer stopStranger()
	if unknown, err := stranger.DialUDPPort(strangerLife, callMediaProbePort); err == nil {
		for i := 0; i < 5; i++ {
			_, _ = unknown.Write([]byte("not-the-approved-peer"))
			time.Sleep(60 * time.Millisecond)
		}
		_ = unknown.Close()
	}
	if got, ok := readDatagram(t, inbound, 800*time.Millisecond); ok {
		t.Fatalf("approved flow received %d bytes that the approved peer did not send", len(got))
	}
	if leaked := flowWithin(flows, 1200*time.Millisecond); leaked != nil {
		_ = leaked.Close()
		t.Fatal("an unapproved WireGuard peer opened a UDP flow")
	}

	// 5. Hang-up: closing the flow and the listener closes the port again.
	_ = inbound.Close()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_, _ = flow.Write([]byte("sent-after-hang-up"))
		time.Sleep(40 * time.Millisecond)
	}
	_ = flow.Close()
	time.Sleep(300 * time.Millisecond)
	reopened, err := server.Listen(ctx, "udp", listenAddress)
	if err != nil {
		t.Fatal("the tunnel UDP port could not be reopened for a later call")
	}
	defer reopened.Close()
	if stale := flowWithin(acceptFlows(reopened), 1200*time.Millisecond); stale != nil {
		_ = stale.Close()
		t.Fatal("datagrams sent after hang-up were delivered to the next listener")
	}
}
