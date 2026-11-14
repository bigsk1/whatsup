package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"tailscale.com/envknob"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/key"
)

func TestPathUsesOnlyExactActivePeerAndNeverConfusesConfiguredRelay(t *testing.T) {
	approved, other := key.NewNode().Public(), key.NewNode().Public()
	for _, test := range []struct {
		name string
		peer *ipnstate.PeerStatus
		want Path
	}{
		{"direct with configured relay", &ipnstate.PeerStatus{PublicKey: approved, Active: true, CurAddr: "synthetic-direct", Relay: "configured"}, PathDirect},
		{"DERP active", &ipnstate.PeerStatus{PublicKey: approved, Active: true, Relay: "synthetic-region"}, PathRelayed},
		{"peer relay", &ipnstate.PeerStatus{PublicKey: approved, Active: true, PeerRelay: "synthetic-peer-relay"}, PathRelayed},
		{"ambiguous includes peer relay", &ipnstate.PeerStatus{PublicKey: approved, Active: true, PeerRelay: "synthetic-peer-relay", CurAddr: "synthetic-direct"}, PathRelayed},
		{"inactive configured relay", &ipnstate.PeerStatus{PublicKey: approved, Relay: "configured"}, PathUnknown},
		{"inactive direct", &ipnstate.PeerStatus{PublicKey: approved, CurAddr: "synthetic-old-direct"}, PathUnknown},
		{"missing path", &ipnstate.PeerStatus{PublicKey: approved, Active: true}, PathUnknown},
		{"wrong peer field", &ipnstate.PeerStatus{PublicKey: other, Active: true, CurAddr: "synthetic-other"}, PathUnknown},
		{"nil peer", nil, PathUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			status := &ipnstate.Status{Peer: map[key.NodePublic]*ipnstate.PeerStatus{approved: test.peer, other: {PublicKey: other, Active: true, CurAddr: "other"}}}
			if peerPath(status, approved) != test.want {
				t.Fatal("path classification crossed peer or transport boundary")
			}
		})
	}
	if peerPath(nil, approved) != PathUnknown || peerPath(&ipnstate.Status{}, approved) != PathUnknown || peerPath(&ipnstate.Status{}, key.NodePublic{}) != PathUnknown {
		t.Fatal("missing status disclosed a path")
	}
	if (*ongoingServerConn)(nil).Path() != PathUnknown || (*ongoingClientConn)(nil).Path() != PathUnknown {
		t.Fatal("zero connection reported a live path")
	}
}

func TestRealOngoingPathReportsBothRolesAndResetsOnClose(t *testing.T) {
	for _, forcedRelay := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "forced relay"}[forcedRelay], func(t *testing.T) {
			if forcedRelay {
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPathForcedRelayWorker$", "-test.timeout=22s")
				command.Env = append(os.Environ(), "WHATSUP_FORCED_RELAY_WORKER=1")
				output, err := command.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 23 || !bytes.Contains(output, []byte("PATH_RELAY_OK\n")) {
					t.Fatal("isolated forced-relay path observation failed")
				}
				return
			}
			profile := loopbackRelayProfile(t)
			owner, joiner := ongoingRecords(t, profile)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			listener, err := NewOngoingListener(ctx, OngoingOptions{Record: owner, Profile: profile, Policy: RelayFallback, AllowRelayFallback: true})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			server, client := ongoingConnectionPair(t, ctx, listener, joiner, profile)
			requireOngoingBytes(t, client, server)
			requireOngoingBytes(t, server, client)
			left, right := server.(*ongoingServerConn), client.(*ongoingClientConn)
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				a, b := left.Path(), right.Path()
				if a != PathUnknown && b != PathUnknown {
					if forcedRelay && (a != PathRelayed || b != PathRelayed) {
						t.Fatal("forced relay reported direct")
					}
					t.Logf("read-only path server=%s client=%s", a, b)
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("active real transport path remained unknown")
				case <-tick.C:
				}
			}
			_ = server.Close()
			_ = client.Close()
			if left.Path() != PathUnknown || right.Path() != PathUnknown {
				t.Fatal("closed stream retained a path")
			}
		})
	}
}

// The upstream force-DERP knob installs blockForeverConn UDP readers whose
// Windows BindClose teardown can hang. Isolate that fixture and terminate its
// disposable process after observation. Normal mode above verifies clean Close.
func TestPathForcedRelayWorker(t *testing.T) {
	if os.Getenv("WHATSUP_FORCED_RELAY_WORKER") != "1" {
		t.Skip("isolated forced-relay fixture only")
	}
	envknob.SetenvForTest(t, "TS_DEBUG_ALWAYS_USE_DERP", "true")
	profile := loopbackRelayProfile(t)
	owner, joiner := ongoingRecords(t, profile)
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	listener, err := NewOngoingListener(ctx, OngoingOptions{Record: owner, Profile: profile, Policy: RelayFallback, AllowRelayFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	server, client := ongoingConnectionPair(t, ctx, listener, joiner, profile)
	requireOngoingBytes(t, client, server)
	requireOngoingBytes(t, server, client)
	if server.(*ongoingServerConn).Path() != PathRelayed || client.(*ongoingClientConn).Path() != PathRelayed {
		t.Fatal("forced relay did not report relayed on both roles")
	}
	fmt.Print("PATH_RELAY_OK\n")
	os.Exit(23)
}
