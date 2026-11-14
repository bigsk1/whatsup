package channel

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func deniedResult(t *testing.T, results <-chan approvalResult) {
	t.Helper()
	select {
	case got := <-results:
		if got.proof != nil || !errors.Is(got.err, ErrDenied) {
			t.Fatalf("authenticated refusal failed to retire the prompt: %v", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refusal left a prompt or frame reader running")
	}
}

func TestDecisionRetirementAfterOneLocalApproval(t *testing.T) {
	for _, voter := range []Role{Inviter, Joiner} {
		t.Run(fmt.Sprint(voter), func(t *testing.T) {
			server, client := pair(t, time.Now().Add(time.Minute))
			sr, cr := reservePair(t, server, client)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			sd, cd := make(chan Decision), make(chan Decision)
			results := make(chan approvalResult, 2)
			go func() { proof, err := sr.WaitApproval(ctx, sd); results <- approvalResult{proof, err} }()
			go func() { proof, err := cr.WaitApproval(ctx, cd); results <- approvalResult{proof, err} }()
			decisions, session := sd, server
			if voter == Joiner {
				decisions, session = cd, client
			}
			// The second unbuffered send cannot be accepted until the first
			// vote has been written. The other person makes no local choice.
			for _, decision := range []Decision{{Approve: true, Fingerprint: session.Peer().Fingerprint()}, {Approve: false}} {
				select {
				case decisions <- decision:
				case <-ctx.Done():
					t.Fatal("the first vote prevented a subsequent refusal")
				}
			}
			deniedResult(t, results)
			deniedResult(t, results)
			for _, candidate := range []*Session{server, client} {
				if err := candidate.WithPendingPeer(func() error { t.Fatal("denied session became pending again"); return nil }); err == nil {
					t.Fatal("denial retained authority")
				}
			}
		})
	}
}

func TestDecisionRetirementSimultaneousDenials(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, cr := reservePair(t, server, client)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sd, cd := make(chan Decision, 1), make(chan Decision, 1)
	sd <- Decision{Approve: false}
	cd <- Decision{Approve: false}
	results := make(chan approvalResult, 2)
	go func() { proof, err := sr.WaitApproval(ctx, sd); results <- approvalResult{proof, err} }()
	go func() { proof, err := cr.WaitApproval(ctx, cd); results <- approvalResult{proof, err} }()
	deniedResult(t, results)
	deniedResult(t, results)
}

func TestDecisionRetirementRejectsUnsolicitedTerminalReceipt(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, _ := reservePair(t, server, client)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan approvalResult, 1)
	go func() { proof, err := sr.WaitApproval(ctx, make(chan Decision)); results <- approvalResult{proof, err} }()
	if err := writePacket(client.state.tls, makePacket("WUDENY01", 4, Joiner, client.state.binding, 0)); err != nil {
		t.Fatal(err)
	}
	got := <-results
	if got.proof != nil || !errors.Is(got.err, ErrProtocol) {
		t.Fatal("unsolicited refusal receipt replaced an explicit decision")
	}
}

func TestDecisionRetirementRejectsMalformedReplayedOrUncleanReceipt(t *testing.T) {
	for _, corrupt := range []string{"magic", "kind", "role", "binding", "replayed-approval", "extra", "unclean"} {
		t.Run(corrupt, func(t *testing.T) {
			server, client := pair(t, time.Now().Add(time.Minute))
			sr, _ := reservePair(t, server, client)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			decisions := make(chan Decision, 1)
			decisions <- Decision{Approve: false}
			results := make(chan approvalResult, 1)
			go func() { proof, err := sr.WaitApproval(ctx, decisions); results <- approvalResult{proof, err} }()
			if _, err := readPacket(client.state.tls, "WUPAIR01", 2, Inviter, client.state.binding, 0); err != nil {
				t.Fatal(err)
			}
			packet := makePacket("WUDENY01", 4, Joiner, client.state.binding, 0)
			switch corrupt {
			case "magic":
				packet[0] ^= 1
			case "kind":
				packet[8] = 3
			case "role":
				packet[9] = byte(Inviter)
			case "binding":
				packet[10] ^= 1
			case "replayed-approval":
				// One earlier approval may be in flight when refusal wins;
				// replaying that decision must never count as the terminal ACK.
				packet = makePacket("WUPAIR01", 1, Joiner, client.state.binding, 0)
			case "extra":
				packet = append(packet, byte(1))
			}
			if err := writePacket(client.state.tls, packet); err != nil {
				t.Fatal(err)
			}
			clear(packet)
			if corrupt == "unclean" || corrupt == "replayed-approval" {
				_ = client.Close()
			}
			got := <-results
			if got.proof != nil || !errors.Is(got.err, ErrProtocol) {
				t.Fatalf("invalid refusal handoff accepted: %v", got.err)
			}
		})
	}
}

func TestDecisionRetirementCancellationJoinsOutstandingReader(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, _ := reservePair(t, server, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	decisions := make(chan Decision, 1)
	decisions <- Decision{Approve: false}
	results := make(chan approvalResult, 1)
	go func() { proof, err := sr.WaitApproval(ctx, decisions); results <- approvalResult{proof, err} }()
	if _, err := readPacket(client.state.tls, "WUPAIR01", 2, Inviter, client.state.binding, 0); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case got := <-results:
		if got.proof != nil || !errors.Is(got.err, ErrCanceled) {
			t.Fatal("canceled terminal wait retained approval")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation failed to join the outstanding frame reader")
	}
}

func TestDecisionReaderLeavesCoalescedNextPhaseFrameUntouched(t *testing.T) {
	server, client := pair(t, time.Now().Add(time.Minute))
	sr, _ := reservePair(t, server, client)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	decisions := make(chan Decision, 1)
	decisions <- Decision{Approve: true, Fingerprint: server.Peer().Fingerprint()}
	results := make(chan approvalResult, 1)
	go func() { proof, err := sr.WaitApproval(ctx, decisions); results <- approvalResult{proof, err} }()
	if err := writePacket(client.state.tls, makePacket("WUPAIR01", 1, Joiner, client.state.binding, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := readPacket(client.state.tls, "WUPAIR01", 1, Inviter, client.state.binding, 0); err != nil {
		t.Fatal(err)
	}
	packet := makePacket("WUREADY1", 3, Joiner, client.state.binding, 0)
	packet = append(packet, makePacket("WUNEXT01", 9, Joiner, client.state.binding, 0)...)
	if err := writePacket(client.state.tls, packet); err != nil {
		t.Fatal(err)
	}
	clear(packet)
	if _, err := readPacket(client.state.tls, "WUREADY1", 3, Inviter, client.state.binding, 0); err != nil {
		t.Fatal(err)
	}
	got := <-results
	if got.proof == nil || got.err != nil {
		t.Fatalf("explicit mutual approval failed: %v", got.err)
	}
	if _, err := readPacket(server.state.tls, "WUNEXT01", 9, Joiner, server.state.binding, 0); err != nil {
		t.Fatal("decision reader consumed the next phase's buffered frame")
	}
}
