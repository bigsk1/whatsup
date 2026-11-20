package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/tailscale/tailcat"

	"whatsup/internal/callmedia"
)

type callMediaOpener interface {
	OpenCallMedia(context.Context) (net.Conn, error)
}

func TestCallMediaDatagramLimitMatchesTheTunnel(t *testing.T) {
	if callmedia.MaxDatagram != tailcat.MaxUDPPayload {
		t.Fatalf("call media builds datagrams up to %d bytes but the tunnel carries %d", callmedia.MaxDatagram, tailcat.MaxUDPPayload)
	}
}

// openCallMediaPair opens the media flow on both ends of one live stream. The
// joiner repeats its first datagram until the inviter's side has the flow, as
// the real media loop does.
func openCallMediaPair(t *testing.T, ctx context.Context, server, client callMediaOpener) (net.Conn, net.Conn) {
	t.Helper()
	type opened struct {
		conn net.Conn
		err  error
	}
	serving := make(chan opened, 1)
	go func() { conn, err := server.OpenCallMedia(ctx); serving <- opened{conn, err} }()
	outbound, err := client.OpenCallMedia(ctx)
	if err != nil {
		t.Fatalf("joiner could not open the call media flow: %v", err)
	}
	for {
		if _, err := outbound.Write([]byte("probe")); err != nil {
			t.Fatalf("joiner could not send on the call media flow: %v", err)
		}
		select {
		case got := <-serving:
			if got.err != nil {
				t.Fatalf("inviter could not open the call media flow: %v", got.err)
			}
			// Drain the bring-up probes.
			for {
				if _, ok := readDatagram(t, got.conn, 300*time.Millisecond); !ok {
					return got.conn, outbound
				}
			}
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("call media flow did not connect in time")
		}
	}
}

func requireCallDatagrams(t *testing.T, a, b net.Conn) {
	t.Helper()
	for _, size := range []int{29, 200, 1200, callmedia.MaxDatagram} {
		payload := make([]byte, size)
		if _, err := rand.Read(payload); err != nil {
			t.Fatal(err)
		}
		for _, direction := range [][2]net.Conn{{a, b}, {b, a}} {
			if _, err := direction[0].Write(payload); err != nil {
				t.Fatalf("could not send a %d-byte call datagram: %v", size, err)
			}
			if got, ok := readDatagram(t, direction[1], 3*time.Second); !ok || !bytes.Equal(got, payload) {
				t.Fatalf("a %d-byte call datagram did not arrive intact (got %d bytes)", size, len(got))
			}
		}
	}
}

func TestRealCallMediaFlowLivesOnlyWhileOpenedOnALiveStream(t *testing.T) {
	profile := loopbackRelayProfile(t)
	owner, joiner := ongoingRecords(t, profile)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	listener, err := NewOngoingListener(ctx, OngoingOptions{Record: owner, Profile: profile, Policy: RelayFallback, AllowRelayFallback: true})
	if err != nil {
		t.Fatal("could not start disposable ongoing transport")
	}
	defer listener.Close()
	serverStream, clientStream := ongoingConnectionPair(t, ctx, listener, joiner, profile)
	server, isServer := serverStream.(callMediaOpener)
	client, isClient := clientStream.(callMediaOpener)
	if !isServer || !isClient {
		t.Fatal("ongoing streams do not offer a call media flow")
	}

	// First call.
	inbound, outbound := openCallMediaPair(t, ctx, server, client)
	requireCallDatagrams(t, inbound, outbound)
	// The chat stream keeps working beside the media flow.
	requireOngoingBytes(t, clientStream, serverStream)
	if again, err := server.OpenCallMedia(ctx); again != nil || !errors.Is(err, ErrCallMedia) {
		t.Fatal("inviter opened a second media flow during a call")
	}
	if again, err := client.OpenCallMedia(ctx); again != nil || !errors.Is(err, ErrCallMedia) {
		t.Fatal("joiner opened a second media flow during a call")
	}

	// Hang-up on the inviter closes the port: later datagrams go nowhere.
	if err := inbound.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := inbound.Write([]byte("after close")); err == nil {
		t.Fatal("a closed media flow still accepted writes")
	}
	for i := 0; i < 5; i++ {
		_, _ = outbound.Write([]byte("sent-after-hang-up"))
		time.Sleep(40 * time.Millisecond)
	}
	_ = outbound.Close()
	time.Sleep(300 * time.Millisecond)

	// Second call on the same stream: a fresh flow, with none of the stale datagrams.
	inbound, outbound = openCallMediaPair(t, ctx, server, client)
	if stale, ok := readDatagram(t, inbound, 500*time.Millisecond); ok {
		t.Fatalf("the next call received %d stale bytes from the previous one", len(stale))
	}
	requireCallDatagrams(t, inbound, outbound)

	// A waiting inviter that is cancelled closes the port and frees the slot.
	_ = inbound.Close()
	_ = outbound.Close()
	waiting, stopWaiting := context.WithCancel(ctx)
	abandoned := make(chan error, 1)
	go func() { _, err := server.OpenCallMedia(waiting); abandoned <- err }()
	time.Sleep(300 * time.Millisecond)
	stopWaiting()
	select {
	case err := <-abandoned:
		if !errors.Is(err, ErrCallMedia) {
			t.Fatalf("a cancelled wait returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled wait for the call media flow did not return")
	}

	// Third call, then the authenticated stream ends: the media flow ends with it.
	inbound, outbound = openCallMediaPair(t, ctx, server, client)
	requireCallDatagrams(t, inbound, outbound)
	if err := serverStream.Close(); err != nil {
		t.Fatal(err)
	}
	_ = inbound.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := inbound.Read(make([]byte, 64)); err == nil {
		t.Fatal("the inviter's media flow outlived its stream")
	}
	if reopened, err := server.OpenCallMedia(ctx); reopened != nil || !errors.Is(err, ErrCallMedia) {
		t.Fatal("a media flow was opened on a closed stream")
	}
	if err := clientStream.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := outbound.Write([]byte("after stream close")); err == nil {
		t.Fatal("the joiner's media flow outlived its stream")
	}
	if reopened, err := client.OpenCallMedia(ctx); reopened != nil || !errors.Is(err, ErrCallMedia) {
		t.Fatal("a media flow was opened on a closed stream")
	}
}
