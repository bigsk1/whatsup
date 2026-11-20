package transport_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"whatsup/internal/call"
	"whatsup/internal/callmedia"
	"whatsup/internal/messaging"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

type integrationCallSink struct {
	mu     sync.Mutex
	frames []callmedia.Frame
	calls  []call.ID
}

func (s *integrationCallSink) CallFrame(_ string, id call.ID, frame callmedia.Frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, frame)
	s.calls = append(s.calls, id)
}
func (*integrationCallSink) CallKeyframeNeeded(string, call.ID)      {}
func (*integrationCallSink) CallReport(string, call.ID, call.Report) {}

func (s *integrationCallSink) audioFor(id call.ID) [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out [][]byte
	for i, frame := range s.frames {
		if s.calls[i] == id && frame.Kind == callmedia.FrameAudio {
			out = append(out, frame.Data)
		}
	}
	return out
}

func waitCallState(t *testing.T, ctx context.Context, manager *messaging.Manager, what string, condition func(call.Snapshot) bool) call.Snapshot {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		snapshot := manager.Call()
		if condition(snapshot) {
			return snapshot
		}
		select {
		case <-ctx.Done():
			t.Fatalf("call did not reach %q over real Tailcat: state %d, last end %d", what, snapshot.State, snapshot.Last.End)
		case <-tick.C:
		}
	}
}

// This is the whole call stack in one process over a real WireGuard tunnel:
// real pairing, real ongoing Tailcat transport through the disposable loopback
// TLS relay, fresh pinned-identity TLS, call signaling in the session loop,
// keys from the TLS exporter, SRTP-sealed audio on the tunnel's UDP flow, and
// teardown. The tunnel is bootstrapped through the loopback relay and both
// nodes run on one host, where the path usually upgrades to direct; the path
// taken is logged, not asserted. This is not a two-machine, NAT, loss, forced
// relay or device test.
func TestVoiceCallOverRealTailcatCarriesSealedAudioAndReleasesThePort(t *testing.T) {
	profile := transport.ExportLoopbackRelayProfileForTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	owner, joiner := newPersistentMessagingPeer(t), newPersistentMessagingPeer(t)
	pairPersistentMessagingPeers(t, ctx, profile, owner, joiner)
	records, err := owner.app.CommittedContacts()
	if err != nil || len(records) != 1 {
		t.Fatal("approved contact was unavailable")
	}
	contact, url := records[0].ID, records[0].RelayServiceURL
	// Unwrapped transport connections: the call media flow is opened on them.
	factories := messaging.Factories{
		Profile: func(ctx context.Context, requested string) (*transport.RelayProfile, error) {
			if ctx.Err() != nil || requested != url {
				return nil, transport.ErrRelayProfile
			}
			return profile, nil
		},
		Listen: func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (messaging.Listener, error) {
			return transport.NewOngoingListener(ctx, transport.OngoingOptions{Record: record, Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: record.RelayFallbackConsent})
		},
		Dial: func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (net.Conn, error) {
			return transport.DialOngoing(ctx, record, profile, transport.RelayFallback, record.RelayFallbackConsent)
		},
	}
	allowVoice := func() (bool, bool) { return true, false }
	ownerSink, joinerSink := &integrationCallSink{}, &integrationCallSink{}
	owner.manager, err = messaging.NewContact(owner.local, owner.app, contact, messaging.ContactOptions{Factories: factories, Calls: messaging.CallOptions{Policy: allowVoice, Sink: ownerSink}})
	if err != nil {
		t.Fatal("could not restore the owner's approved contact")
	}
	joiner.manager, err = messaging.NewContact(joiner.local, joiner.app, contact, messaging.ContactOptions{Factories: factories, Calls: messaging.CallOptions{Policy: allowVoice, Sink: joinerSink}})
	if err != nil {
		t.Fatal("could not restore the joiner's approved contact")
	}
	for _, manager := range []*messaging.Manager{owner.manager, joiner.manager} {
		if _, err := manager.Connect(contact); err != nil {
			t.Fatal("explicit connect was rejected")
		}
	}
	for _, manager := range []*messaging.Manager{owner.manager, joiner.manager} {
		waitMessaging(t, ctx, manager, func(s messaging.Snapshot) bool { return s.State == "connected" })
	}

	voiceCall := func(caller, callee *messaging.Manager, callerSink, calleeSink *integrationCallSink, tag byte) call.ID {
		t.Helper()
		var placed call.Snapshot
		for {
			placed, err = caller.StartCall(ctx, false, 0)
			if !errors.Is(err, messaging.ErrUnavailable) {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("the authenticated session never became call-capable")
			case <-time.After(10 * time.Millisecond):
			}
		}
		if err != nil || placed.State != call.Outgoing {
			t.Fatalf("call was not placed over the real session: %v", err)
		}
		ringing := waitCallState(t, ctx, callee, "incoming", func(s call.Snapshot) bool { return s.State == call.Incoming })
		if ringing.Call != placed.Call {
			t.Fatal("the callee rang for a different call")
		}
		if _, err := callee.AnswerCall(ctx, placed.Call, false, 0); err != nil {
			t.Fatalf("answer was refused: %v", err)
		}
		// Active means an SRTP-authentic datagram arrived on the tunnel UDP flow in each direction.
		waitCallState(t, ctx, caller, "active", func(s call.Snapshot) bool { return s.State == call.Active })
		waitCallState(t, ctx, callee, "active", func(s call.Snapshot) bool { return s.State == call.Active })

		const frames = 50
		frame := func(i int, from byte) []byte {
			return append([]byte{tag, from, byte(i)}, bytes.Repeat([]byte{byte(i)}, 77)...)
		}
		for i := 1; i <= frames; i++ {
			if err := caller.SendCallAudio(placed.Call, frame(i, 1), uint32(i)*callmedia.AudioFrameSamples); err != nil {
				t.Fatalf("caller could not send audio frame %d: %v", i, err)
			}
			if err := callee.SendCallAudio(placed.Call, frame(i, 2), uint32(i)*callmedia.AudioFrameSamples); err != nil {
				t.Fatalf("callee could not send audio frame %d: %v", i, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		for _, side := range []struct {
			sink *integrationCallSink
			from byte
		}{{calleeSink, 1}, {callerSink, 2}} {
			var got [][]byte
			deadline := time.Now().Add(5 * time.Second)
			for {
				if got = side.sink.audioFor(placed.Call); len(got) >= frames || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if len(got) != frames {
				t.Fatalf("%d of %d sealed audio frames arrived over the tunnel", len(got), frames)
			}
			for i, data := range got {
				if !bytes.Equal(data, frame(i+1, side.from)) {
					t.Fatalf("audio frame %d arrived damaged or out of order", i+1)
				}
			}
		}
		return placed.Call
	}

	// First call, placed by the dialing side.
	first := voiceCall(joiner.manager, owner.manager, joinerSink, ownerSink, 0xA1)
	t.Logf("observed path during the first call: %s", owner.manager.Snapshot().Path)
	// Chat keeps working during the call.
	if _, err := owner.manager.Send(contact, "synthetic text during a call"); err != nil {
		t.Fatal("text could not be queued during a call")
	}
	waitMessaging(t, ctx, joiner.manager, func(s messaging.Snapshot) bool {
		_, arrived := messagingMessage(s, "synthetic text during a call")
		return arrived
	})
	if joiner.manager.Call().State != call.Active {
		t.Fatal("chat traffic disturbed the call")
	}
	if _, err := owner.manager.EndCall(ctx, first); err != nil {
		t.Fatalf("hang-up was refused: %v", err)
	}
	waitCallState(t, ctx, owner.manager, "hung up", func(s call.Snapshot) bool { return s.State == call.Idle && s.Last.End == call.EndHungUp })
	last := waitCallState(t, ctx, joiner.manager, "peer hung up", func(s call.Snapshot) bool { return s.State == call.Idle && s.Last.End == call.EndPeerHungUp }).Last
	if !last.Connected || last.Call != first {
		t.Fatal("the finished call was not recorded as connected")
	}
	if err := joiner.manager.SendCallAudio(first, []byte{1}, 0); !errors.Is(err, messaging.ErrUnavailable) {
		t.Fatal("audio was accepted after hang-up")
	}

	// Second call on the same session, placed by the listening side: the port
	// reopens, the keys are new, and nothing from the first call leaks in.
	time.Sleep(call.DefaultMinOfferGap)
	second := voiceCall(owner.manager, joiner.manager, ownerSink, joinerSink, 0xB2)
	if second == first {
		t.Fatal("a call ID was reused")
	}

	// The same side calls again, twice from each side. Alternating callers
	// alone hides state that is left behind for the next call in the same
	// direction.
	hangUp := func(ender, other *messaging.Manager, id call.ID) {
		t.Helper()
		if _, err := ender.EndCall(ctx, id); err != nil {
			t.Fatalf("hang-up was refused: %v", err)
		}
		waitCallState(t, ctx, ender, "hung up", func(s call.Snapshot) bool { return s.State == call.Idle && s.Last.Call == id })
		waitCallState(t, ctx, other, "peer hung up", func(s call.Snapshot) bool { return s.State == call.Idle && s.Last.Call == id })
		time.Sleep(call.DefaultMinOfferGap)
	}
	hangUp(owner.manager, joiner.manager, second)
	third := voiceCall(owner.manager, joiner.manager, ownerSink, joinerSink, 0xC3)
	hangUp(joiner.manager, owner.manager, third)
	fourth := voiceCall(joiner.manager, owner.manager, joinerSink, ownerSink, 0xD4)
	hangUp(joiner.manager, owner.manager, fourth)
	fifth := voiceCall(joiner.manager, owner.manager, joinerSink, ownerSink, 0xE5)
	second = fifth

	// The session ends during the call: the call ends with it.
	joiner.manager.Close()
	joiner.manager = nil
	waitCallState(t, ctx, owner.manager, "connection lost", func(s call.Snapshot) bool { return s.State == call.Idle && s.Last.End == call.EndConnectionLost })
	if err := owner.manager.SendCallAudio(second, []byte{1}, 0); !errors.Is(err, messaging.ErrUnavailable) {
		t.Fatal("audio was accepted after the session ended")
	}
}
