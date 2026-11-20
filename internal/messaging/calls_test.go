package messaging

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/call"
	"whatsup/internal/callmedia"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

// These tests run two or three real Managers with real pinned TLS, the real
// call state machine and real SRTP sealing. The authenticated stream is a
// net.Pipe and the media flow is an in-memory datagram link with the same
// contract as the transport's: nothing is delivered unless the receiving side
// has the flow open or is waiting to accept it. They are not Tailcat, relay or
// device evidence; the transport package tests the real media flow.

type mediaHub struct {
	mu     sync.Mutex
	toward [2]chan []byte // 0: toward the inviter, 1: toward the joiner
	open   [2]bool
	opens  [2]int
	closes [2]int
	drop   func(towardInviter bool, count int) bool
	sent   [2]int
	// closeDelay makes closing a flow slow, so a caller that returns without
	// waiting for the media loop is caught with the flow still open.
	closeDelay time.Duration
	// detached stops the stream's own Close from closing the flow, leaving
	// that to the manager's media loop alone. The real transport closes the
	// flow in both places; this isolates the manager's part.
	detached bool
}

func newMediaHub() *mediaHub {
	return &mediaHub{toward: [2]chan []byte{make(chan []byte, 512), make(chan []byte, 512)}}
}

func (h *mediaHub) openFlows() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.opens[0] + h.opens[1] - h.closes[0] - h.closes[1]
}

type memFlow struct {
	hub      *mediaHub
	side     int
	pending  []byte
	closed   chan struct{}
	once     sync.Once
	deadline atomic.Int64
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }
func (timeoutError) Unwrap() error   { return os.ErrDeadlineExceeded }

func (f *memFlow) Read(p []byte) (int, error) {
	if f.pending != nil {
		n := copy(p, f.pending)
		f.pending = nil
		return n, nil
	}
	var timer <-chan time.Time
	if at := f.deadline.Load(); at != 0 {
		wait := time.Until(time.Unix(0, at))
		if wait <= 0 {
			return 0, timeoutError{}
		}
		t := time.NewTimer(wait)
		defer t.Stop()
		timer = t.C
	}
	select {
	case datagram := <-f.hub.toward[f.side]:
		return copy(p, datagram), nil
	case <-f.closed:
		return 0, net.ErrClosed
	case <-timer:
		return 0, timeoutError{}
	}
}

func (f *memFlow) Write(p []byte) (int, error) {
	select {
	case <-f.closed:
		return 0, net.ErrClosed
	default:
	}
	other := 1 - f.side
	f.hub.mu.Lock()
	f.hub.sent[other]++
	deliver := f.hub.open[other] && (f.hub.drop == nil || !f.hub.drop(other == 0, f.hub.sent[other]))
	f.hub.mu.Unlock()
	if deliver {
		select {
		case f.hub.toward[other] <- bytes.Clone(p):
		default: // a full queue is ordinary loss
		}
	}
	return len(p), nil
}

func (f *memFlow) Close() error {
	f.once.Do(func() {
		close(f.closed)
		f.hub.mu.Lock()
		delay := f.hub.closeDelay
		f.hub.mu.Unlock()
		time.Sleep(delay)
		f.hub.mu.Lock()
		f.hub.open[f.side] = false
		f.hub.closes[f.side]++
		f.hub.mu.Unlock()
		for {
			select {
			case <-f.hub.toward[f.side]:
			default:
				return
			}
		}
	})
	return nil
}
func (f *memFlow) LocalAddr() net.Addr              { return nil }
func (f *memFlow) RemoteAddr() net.Addr             { return nil }
func (f *memFlow) SetDeadline(t time.Time) error    { return f.SetReadDeadline(t) }
func (f *memFlow) SetWriteDeadline(time.Time) error { return nil }
func (f *memFlow) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		f.deadline.Store(0)
	} else {
		f.deadline.Store(t.UnixNano())
	}
	return nil
}

// callPipeConn is one end of an authenticated-stream pipe that can also open
// the call media flow, like the transport's ongoing connections.
type callPipeConn struct {
	net.Conn
	hub  *mediaHub
	side int
	mu   sync.Mutex
	flow *memFlow
}

func (c *callPipeConn) OpenCallMedia(ctx context.Context) (net.Conn, error) {
	c.mu.Lock()
	if c.flow != nil {
		select {
		case <-c.flow.closed:
		default:
			c.mu.Unlock()
			return nil, transport.ErrCallMedia
		}
	}
	flow := &memFlow{hub: c.hub, side: c.side, closed: make(chan struct{})}
	c.flow = flow
	c.mu.Unlock()
	c.hub.mu.Lock()
	c.hub.open[c.side] = true
	c.hub.opens[c.side]++
	c.hub.mu.Unlock()
	if c.side == 0 {
		// The inviter's flow exists once the joiner's first datagram arrives.
		select {
		case first := <-c.hub.toward[0]:
			flow.pending = first
		case <-ctx.Done():
			_ = flow.Close()
			return nil, transport.ErrCallMedia
		}
	}
	return flow, nil
}

func (c *callPipeConn) Close() error {
	c.mu.Lock()
	flow := c.flow
	c.mu.Unlock()
	c.hub.mu.Lock()
	detached := c.hub.detached
	c.hub.mu.Unlock()
	if flow != nil && !detached {
		_ = flow.Close()
	}
	return c.Conn.Close()
}

type callNetwork struct {
	mu        sync.Mutex
	listeners map[string]*registryPipeListener
	hubs      map[string]*mediaHub
}

func (n *callNetwork) hub(id string) *mediaHub {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.hubs[id]
}

func (n *callNetwork) factories() Factories {
	return Factories{
		Profile: func(context.Context, string) (*transport.RelayProfile, error) { return nil, nil },
		Listen: func(_ context.Context, record storage.PendingRecord, _ *transport.RelayProfile) (Listener, error) {
			l := &registryPipeListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
			n.mu.Lock()
			if n.listeners == nil {
				n.listeners, n.hubs = map[string]*registryPipeListener{}, map[string]*mediaHub{}
			}
			n.listeners[record.ID] = l
			n.mu.Unlock()
			return l, nil
		},
		Dial: func(ctx context.Context, record storage.PendingRecord, _ *transport.RelayProfile) (net.Conn, error) {
			n.mu.Lock()
			l := n.listeners[record.ID]
			n.mu.Unlock()
			if l == nil {
				return nil, ErrUnavailable
			}
			left, right := net.Pipe()
			hub := newMediaHub()
			select {
			case <-ctx.Done():
				left.Close()
				right.Close()
				return nil, ctx.Err()
			case <-l.closed:
				left.Close()
				right.Close()
				return nil, net.ErrClosed
			case l.incoming <- &callPipeConn{Conn: left, hub: hub, side: 0}:
				n.mu.Lock()
				n.hubs[record.ID] = hub
				n.mu.Unlock()
				return &callPipeConn{Conn: right, hub: hub, side: 1}, nil
			}
		},
	}
}

type recordedFrame struct {
	contact string
	id      call.ID
	frame   callmedia.Frame
}

type callRecorder struct {
	mu        sync.Mutex
	frames    []recordedFrame
	keyframes int
	reports   int
}

func (r *callRecorder) CallFrame(contact string, id call.ID, frame callmedia.Frame) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames = append(r.frames, recordedFrame{contact, id, frame})
}
func (r *callRecorder) CallKeyframeNeeded(string, call.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keyframes++
}
func (r *callRecorder) CallReport(string, call.ID, call.Report) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports++
}
func (r *callRecorder) count(kind callmedia.Kind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, f := range r.frames {
		if f.frame.Kind == kind {
			n++
		}
	}
	return n
}
func (r *callRecorder) keyframeRequests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keyframes
}

type callPeer struct {
	*registryPeer
	sink   *callRecorder
	policy atomic.Int32 // 0 off, 1 voice, 2 voice and video
}

func newCallPeer(t *testing.T, policy int32) *callPeer {
	p := &callPeer{registryPeer: newRegistryPeer(t), sink: &callRecorder{}}
	p.policy.Store(policy)
	return p
}

func (p *callPeer) open(t *testing.T, n *callNetwork, tune func(*CallOptions)) {
	t.Helper()
	options := CallOptions{Sink: p.sink, Policy: func() (bool, bool) { level := p.policy.Load(); return level >= 1, level >= 2 }}
	if tune != nil {
		tune(&options)
	}
	r, err := NewRegistry(p.local, p.app, RegistryOptions{Revisions: p.revisions, Factories: n.factories(), Calls: options})
	if err != nil {
		t.Fatal(err)
	}
	p.registry = r
}

func waitCall(t *testing.T, p *callPeer, id string, what string, predicate func(call.Snapshot) bool) call.Snapshot {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		snapshot := p.registry.Call(id)
		if predicate(snapshot) {
			return snapshot
		}
		if time.Now().After(deadline) {
			t.Fatalf("call did not reach %q: state %d, last end %d", what, snapshot.State, snapshot.Last.End)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

func isState(state call.State) func(call.Snapshot) bool {
	return func(s call.Snapshot) bool { return s.State == state }
}

func endedAs(end call.End) func(call.Snapshot) bool {
	return func(s call.Snapshot) bool { return s.State == call.Idle && s.Last.End == end }
}

// connectedCallPair returns an inviter and a joiner with a live call-capable session.
func connectedCallPair(t *testing.T, inviterPolicy, joinerPolicy int32, tune func(*CallOptions)) (inviter, joiner *callPeer, id string, network *callNetwork) {
	t.Helper()
	inviter, joiner = newCallPeer(t, inviterPolicy), newCallPeer(t, joinerPolicy)
	id = seedRegistryAssociation(t, inviter.registryPeer, joiner.registryPeer)
	network = &callNetwork{}
	inviter.open(t, network, tune)
	joiner.open(t, network, tune)
	connectRegistryPair(t, inviter.registryPeer, joiner.registryPeer, id)
	return inviter, joiner, id, network
}

func startCall(t *testing.T, caller *callPeer, id string, video bool, codecs call.Codec) call.ID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The session reports connected just before its call path is installed.
	var snapshot call.Snapshot
	var err error
	waitFor(t, "a call-capable session", func() bool {
		snapshot, err = caller.registry.StartCall(ctx, id, video, codecs)
		return !errors.Is(err, ErrUnavailable)
	})
	if err != nil || snapshot.State != call.Outgoing || snapshot.Call.IsZero() {
		t.Fatalf("call was not placed: %v", err)
	}
	return snapshot.Call
}

func establish(t *testing.T, caller, callee *callPeer, id string, video bool, codecs call.Codec) call.ID {
	t.Helper()
	callID := startCall(t, caller, id, video, codecs)
	ringing := waitCall(t, callee, id, "incoming", isState(call.Incoming))
	if ringing.Call != callID || ringing.Outgoing {
		t.Fatal("callee is ringing for a different call")
	}
	waitCall(t, caller, id, "peer ringing", func(s call.Snapshot) bool { return s.PeerRinging })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := callee.registry.AnswerCall(ctx, id, callID, video, codecs); err != nil {
		t.Fatalf("answer refused: %v", err)
	}
	waitCall(t, caller, id, "active", isState(call.Active))
	waitCall(t, callee, id, "active", isState(call.Active))
	return callID
}

func background() context.Context { return context.Background() }

func TestVoiceCallBetweenTwoManagersCarriesSealedAudioAndEndsCleanly(t *testing.T) {
	for _, placedByInviter := range []bool{true, false} {
		inviter, joiner, id, network := connectedCallPair(t, 1, 1, nil)
		caller, callee := inviter, joiner
		if !placedByInviter {
			caller, callee = joiner, inviter
		}
		callID := establish(t, caller, callee, id, false, 0)
		if network.hub(id).openFlows() != 2 {
			t.Fatal("an active call does not have exactly one media flow per side")
		}
		for i := 1; i <= 25; i++ {
			frame := bytes.Repeat([]byte{byte(i)}, 80)
			if err := caller.registry.SendCallAudio(id, callID, frame, uint32(i)*callmedia.AudioFrameSamples); err != nil {
				t.Fatalf("caller could not send audio: %v", err)
			}
			if err := callee.registry.SendCallAudio(id, callID, frame, uint32(i)*callmedia.AudioFrameSamples); err != nil {
				t.Fatalf("callee could not send audio: %v", err)
			}
		}
		for _, p := range []*callPeer{caller, callee} {
			waitFor(t, "25 audio frames", func() bool { return p.sink.count(callmedia.FrameAudio) == 25 })
			p.sink.mu.Lock()
			for i, got := range p.sink.frames {
				if got.contact != id || got.id != callID || got.frame.Recovered || !bytes.Equal(got.frame.Data, bytes.Repeat([]byte{byte(i + 1)}, 80)) {
					t.Fatalf("audio frame %d arrived wrong", i+1)
				}
			}
			p.sink.mu.Unlock()
		}
		if err := caller.registry.SendCallVideo(id, callID, []byte{1, 2, 3}, 0, true); !errors.Is(err, ErrUnavailable) {
			t.Fatal("video was sent on a voice call")
		}
		if _, err := callee.registry.SetCallMuted(background(), id, callID, true); err != nil {
			t.Fatal(err)
		}
		waitCall(t, caller, id, "peer muted", func(s call.Snapshot) bool { return s.PeerMicMuted })

		if _, err := caller.registry.EndCall(background(), id, callID); err != nil {
			t.Fatal(err)
		}
		waitCall(t, caller, id, "hung up", endedAs(call.EndHungUp))
		last := waitCall(t, callee, id, "peer hung up", endedAs(call.EndPeerHungUp)).Last
		if !last.Connected || last.Call != callID {
			t.Fatal("finished call was not recorded as connected")
		}
		waitFor(t, "media flows to close", func() bool { return network.hub(id).openFlows() == 0 })
		if err := caller.registry.SendCallAudio(id, callID, []byte{1}, 0); !errors.Is(err, ErrUnavailable) {
			t.Fatal("audio was accepted after hang-up")
		}
		// The chat session is untouched by the call.
		if _, err := caller.registry.Send(id, "after the call"); err != nil {
			t.Fatal(err)
		}
		waitRegistry(t, callee.registryPeer, id, func(s Snapshot) bool {
			return len(s.Messages) == 1 && s.Messages[0].Text == "after the call"
		})
		// A second call on the same session gets fresh keys and works.
		second := establish(t, callee, caller, id, false, 0)
		if second == callID {
			t.Fatal("a call ID was reused")
		}
		if err := callee.registry.SendCallAudio(id, second, []byte{9, 9, 9}, 960); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "audio on the second call", func() bool { return caller.sink.count(callmedia.FrameAudio) == 26 })
	}
}

func TestCallsSwitchedOffRefuseWithoutRingingAndCannotBePlaced(t *testing.T) {
	inviter, joiner, id, network := connectedCallPair(t, 1, 0, nil)
	// Let the freshly connected session finish its own profile exchange first.
	revision := joiner.registry.Snapshot(id).Revision
	for settled := time.Now(); time.Since(settled) < 400*time.Millisecond; time.Sleep(10 * time.Millisecond) {
		if now := joiner.registry.Snapshot(id).Revision; now != revision {
			revision, settled = now, time.Now()
		}
	}
	startCall(t, inviter, id, false, 0)
	waitCall(t, inviter, id, "peer unavailable", endedAs(call.EndPeerUnavailable))
	if got := joiner.registry.Call(id); got.State != call.Idle || !got.Call.IsZero() || got.Last.End != call.EndNone {
		t.Fatal("an offer left a trace on a device with calls switched off")
	}
	if joiner.registry.Snapshot(id).Revision != revision {
		t.Fatal("a refused offer notified the interface")
	}
	if network.hub(id).openFlows() != 0 {
		t.Fatal("a refused call opened a media flow")
	}
	// The device with calls off cannot place one either.
	if _, err := joiner.registry.StartCall(background(), id, false, 0); !errors.Is(err, call.ErrNotAllowed) {
		t.Fatalf("a call was placed with calls switched off: %v", err)
	}
	// Video off on the callee: a video offer rings as voice and cannot be answered as video.
	joiner.policy.Store(1)
	inviter.policy.Store(2)
	time.Sleep(call.DefaultMinOfferGap)
	callID := startCall(t, inviter, id, true, call.VP8)
	if ringing := waitCall(t, joiner, id, "incoming", isState(call.Incoming)); ringing.Media != call.Audio {
		t.Fatal("a video offer was presented as video with video switched off")
	}
	if _, err := joiner.registry.AnswerCall(background(), id, callID, true, call.VP8); !errors.Is(err, call.ErrNotAllowed) {
		t.Fatal("video was accepted with video switched off")
	}
	if _, err := joiner.registry.AnswerCall(background(), id, callID, false, 0); err != nil {
		t.Fatal(err)
	}
	if active := waitCall(t, inviter, id, "active", isState(call.Active)); active.Media != call.Audio {
		t.Fatal("caller did not fall back to a voice call")
	}
}

func TestOnlyOneCallPerDeviceAcrossContacts(t *testing.T) {
	a, b, c := newCallPeer(t, 1), newCallPeer(t, 1), newCallPeer(t, 1)
	withB := seedRegistryAssociation(t, a.registryPeer, b.registryPeer)
	withC := seedRegistryAssociation(t, c.registryPeer, a.registryPeer)
	network := &callNetwork{}
	a.open(t, network, nil)
	b.open(t, network, nil)
	c.open(t, network, nil)
	connectRegistryPair(t, a.registryPeer, b.registryPeer, withB)
	connectRegistryPair(t, c.registryPeer, a.registryPeer, withC)

	callID := establish(t, a, b, withB, false, 0)
	// A third person calls the busy device.
	startCall(t, c, withC, false, 0)
	waitCall(t, c, withC, "peer busy", endedAs(call.EndPeerBusy))
	if got := a.registry.Call(withC); got.State != call.Idle {
		t.Fatal("a busy device rang for a second contact")
	}
	// The busy device cannot place a second call itself.
	if _, err := a.registry.StartCall(background(), withC, false, 0); !errors.Is(err, call.ErrBusy) {
		t.Fatalf("a second call was placed from a busy device: %v", err)
	}
	if got := a.registry.Call(withB); got.State != call.Active || got.Call != callID {
		t.Fatal("the existing call was disturbed")
	}
	// After hanging up, the device is free for the other contact.
	if _, err := a.registry.EndCall(background(), withB, callID); err != nil {
		t.Fatal(err)
	}
	waitCall(t, b, withB, "ended", isState(call.Idle))
	time.Sleep(call.DefaultMinOfferGap)
	establish(t, c, a, withC, false, 0)
}

func TestCallEndsAndMediaIsJoinedWhenTheSessionEnds(t *testing.T) {
	for _, how := range []string{"peer closes", "local pause", "local close"} {
		inviter, joiner, id, network := connectedCallPair(t, 1, 1, nil)
		establish(t, inviter, joiner, id, false, 0)
		hub := network.hub(id)
		hub.mu.Lock()
		hub.closeDelay, hub.detached = 200*time.Millisecond, true
		hub.mu.Unlock()
		switch how {
		case "peer closes":
			joiner.registry.Close()
			waitCall(t, inviter, id, "connection lost", endedAs(call.EndConnectionLost))
		case "local pause":
			if _, err := inviter.registry.SetPaused(id, true); err != nil {
				t.Fatal(err)
			}
			// Pause returns only after this contact's workers have stopped.
			hub.mu.Lock()
			still := hub.opens[0] - hub.closes[0]
			hub.mu.Unlock()
			if still != 0 {
				t.Fatal("pause returned while the call's media flow was still open")
			}
			waitCall(t, joiner, id, "connection lost", endedAs(call.EndConnectionLost))
		case "local close":
			inviter.registry.Close()
			hub.mu.Lock()
			still := hub.opens[0] - hub.closes[0]
			hub.mu.Unlock()
			if still != 0 {
				t.Fatal("close returned while the call's media flow was still open")
			}
			waitCall(t, joiner, id, "connection lost", endedAs(call.EndConnectionLost))
		}
		waitFor(t, how+": both media flows closed", func() bool { return hub.openFlows() == 0 })
	}
}

func TestUnansweredCallTimesOutThroughTheSessionLoop(t *testing.T) {
	inviter, joiner, id, _ := connectedCallPair(t, 1, 1, func(o *CallOptions) { o.RingTimeout = 400 * time.Millisecond })
	startCall(t, joiner, id, false, 0)
	waitCall(t, inviter, id, "incoming", isState(call.Incoming))
	waitCall(t, joiner, id, "no answer", endedAs(call.EndNoAnswer))
	waitCall(t, inviter, id, "missed", endedAs(call.EndMissed))
}

func TestStalledMediaEndsTheCallOnBothSides(t *testing.T) {
	inviter, joiner, id, network := connectedCallPair(t, 1, 1, func(o *CallOptions) { o.StallTimeout = 700 * time.Millisecond })
	establish(t, inviter, joiner, id, false, 0)
	hub := network.hub(id)
	hub.mu.Lock()
	hub.drop = func(towardInviter bool, _ int) bool { return towardInviter } // the inviter stops hearing anything
	hub.mu.Unlock()
	waitCall(t, inviter, id, "media failed", endedAs(call.EndMediaFailed))
	waitCall(t, joiner, id, "told media failed", endedAs(call.EndMediaFailed))
	waitFor(t, "media flows to close", func() bool { return hub.openFlows() == 0 })
}

func TestMediaThatNeverStartsEndsTheCall(t *testing.T) {
	inviter, joiner, id, network := connectedCallPair(t, 1, 1, func(o *CallOptions) { o.ConnectTimeout = 500 * time.Millisecond })
	callID := startCall(t, inviter, id, false, 0)
	waitCall(t, joiner, id, "incoming", isState(call.Incoming))
	hub := network.hub(id)
	hub.mu.Lock()
	hub.drop = func(bool, int) bool { return true } // no datagram ever arrives
	hub.mu.Unlock()
	if _, err := joiner.registry.AnswerCall(background(), id, callID, false, 0); err != nil {
		t.Fatal(err)
	}
	waitCall(t, inviter, id, "media failed", endedAs(call.EndMediaFailed))
	waitCall(t, joiner, id, "media failed", endedAs(call.EndMediaFailed))
	waitFor(t, "media flows to close", func() bool { return hub.openFlows() == 0 })
}

func TestVideoCallNegotiatesACodecAndRequestsAKeyframeAfterLoss(t *testing.T) {
	inviter, joiner, id, network := connectedCallPair(t, 2, 2, nil)
	callID := startCall(t, inviter, id, true, call.VP8|call.H264)
	waitCall(t, joiner, id, "incoming", isState(call.Incoming))
	if _, err := joiner.registry.AnswerCall(background(), id, callID, true, call.VP9|call.H264); err != nil {
		t.Fatal(err)
	}
	for _, p := range []*callPeer{inviter, joiner} {
		if active := waitCall(t, p, id, "active", isState(call.Active)); active.Media != call.Video || active.Codec != call.H264 {
			t.Fatalf("video call did not settle on the shared codec: media %d codec %d", active.Media, active.Codec)
		}
	}
	keyframe := bytes.Repeat([]byte{0xAB}, 5000)
	if err := inviter.registry.SendCallVideo(id, callID, keyframe, 3000, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the keyframe", func() bool { return joiner.sink.count(callmedia.FrameVideo) == 1 })
	joiner.sink.mu.Lock()
	first := joiner.sink.frames[len(joiner.sink.frames)-1].frame
	joiner.sink.mu.Unlock()
	if !first.Key || !bytes.Equal(first.Data, keyframe) {
		t.Fatal("the keyframe did not arrive intact")
	}
	// Lose one fragment of the next frame on its way to the joiner.
	hub := network.hub(id)
	var once sync.Once
	hub.mu.Lock()
	hub.drop = func(towardInviter bool, _ int) bool {
		lost := false
		if !towardInviter {
			once.Do(func() { lost = true })
		}
		return lost
	}
	hub.mu.Unlock()
	waitFor(t, "a damaged frame and the keyframe request", func() bool {
		_ = inviter.registry.SendCallVideo(id, callID, bytes.Repeat([]byte{0xCD}, 5000), 6000, false)
		time.Sleep(20 * time.Millisecond)
		return inviter.sink.keyframeRequests() > 0
	})
	if joiner.sink.count(callmedia.FrameVideo) != 1 {
		t.Fatal("an undecodable frame was delivered")
	}
	if err := inviter.registry.SendCallVideo(id, callID, keyframe, 9000, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the repairing keyframe", func() bool { return joiner.sink.count(callmedia.FrameVideo) == 2 })
}

func TestCallActionsNeedALiveCallCapableSession(t *testing.T) {
	inviter, joiner, id, _ := connectedCallPair(t, 1, 1, nil)
	callID := startCall(t, inviter, id, false, 0)
	waitCall(t, joiner, id, "incoming", isState(call.Incoming))
	if _, err := inviter.registry.AnswerCall(background(), id, callID, false, 0); !errors.Is(err, call.ErrUnavailable) {
		t.Fatal("the caller could answer its own call")
	}
	var wrong call.ID
	wrong[0] = 0xEE
	if _, err := joiner.registry.EndCall(background(), id, wrong); !errors.Is(err, call.ErrUnavailable) {
		t.Fatal("a stale call ID ended the current call")
	}
	if _, err := joiner.registry.StartCall(background(), "AAAAAAAAAAAAAAAAAAAAAA", false, 0); err == nil {
		t.Fatal("a call was placed to an unknown contact")
	}
	// A peer that did not negotiate the call protocol can neither be called nor call.
	m, err := inviter.registry.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.capabilities.Calls = false
	m.mu.Unlock()
	if _, err := inviter.registry.EndCall(background(), id, callID); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a call action ran without the negotiated capability")
	}
	// With no session at all, nothing is queued.
	joiner.registry.Close()
	waitRegistry(t, inviter.registryPeer, id, func(s Snapshot) bool { return s.State != "connected" })
	if _, err := inviter.registry.StartCall(background(), id, false, 0); !errors.Is(err, ErrUnavailable) {
		t.Fatal("a call was accepted for later with the contact offline")
	}
}

func TestCallPacketsRequireTheNegotiatedCapability(t *testing.T) {
	var id call.ID
	id[0] = 1
	encoded, err := call.Encode(call.Frame{Kind: call.KindOffer, Call: id, Media: call.Audio})
	if err != nil {
		t.Fatal(err)
	}
	wire := append([]byte{callPacket}, encoded...)
	if p, err := readPacketFor(bytes.NewReader(wire), sessionauth.Capabilities{WireVersion: 4, Attachments: true, Control: true, Withdrawals: true, Avatars: true, Calls: true}); err != nil || p.kind != callPacket || p.call.Kind != call.KindOffer {
		t.Fatal("a call packet was not decoded on a call-capable session")
	}
	if _, err := readPacketFor(bytes.NewReader(wire), sessionauth.Capabilities{WireVersion: 4, Attachments: true, Control: true, Withdrawals: true, Avatars: true}); err == nil {
		t.Fatal("a call packet was accepted from a peer that did not negotiate calls")
	}
	wire[len(wire)-2] = 9 // unknown media
	if _, err := readPacketFor(bytes.NewReader(wire), sessionauth.Capabilities{Calls: true}); err == nil {
		t.Fatal("a malformed call frame was accepted")
	}
}

func TestSwitchingCallsOffEndsALiveCall(t *testing.T) {
	inviter, joiner, id, network := connectedCallPair(t, 1, 1, nil)
	establish(t, inviter, joiner, id, false, 0)
	inviter.policy.Store(0)
	inviter.registry.CallSettingsChanged(background())
	waitCall(t, inviter, id, "interrupted", endedAs(call.EndInterrupted))
	waitCall(t, joiner, id, "peer hung up", endedAs(call.EndPeerHungUp))
	waitFor(t, "media flows to close", func() bool { return network.hub(id).openFlows() == 0 })

	// A ringing call on the device that switches calls off stops ringing at once.
	inviter.policy.Store(1)
	time.Sleep(call.DefaultMinOfferGap)
	startCall(t, joiner, id, false, 0)
	waitCall(t, inviter, id, "incoming", isState(call.Incoming))
	inviter.policy.Store(0)
	inviter.registry.CallSettingsChanged(background())
	waitCall(t, inviter, id, "interrupted", endedAs(call.EndInterrupted))
	waitCall(t, joiner, id, "peer unavailable", endedAs(call.EndPeerUnavailable))
	// Unchanged settings and contacts without a call are left alone.
	joiner.registry.CallSettingsChanged(background())
	if got := joiner.registry.Call(id); got.State != call.Idle || got.Last.End != call.EndPeerUnavailable {
		t.Fatal("applying unchanged settings altered the call record")
	}
}

// Calls are switched off while the call is ringing on the other side, and the
// answer arrives before anything else. The answer must not start media here.
func TestALateAnswerStartsNoMediaAfterCallsAreSwitchedOff(t *testing.T) {
	inviter, joiner, id, network := connectedCallPair(t, 1, 1, nil)
	callID := startCall(t, inviter, id, false, 0)
	waitCall(t, joiner, id, "incoming", isState(call.Incoming))
	inviter.policy.Store(0) // no settings-changed call: only the answer arrives
	if _, err := joiner.registry.AnswerCall(background(), id, callID, false, 0); err != nil {
		t.Fatal(err)
	}
	waitCall(t, inviter, id, "interrupted", endedAs(call.EndInterrupted))
	waitCall(t, joiner, id, "peer hung up", endedAs(call.EndPeerHungUp))
	hub := network.hub(id)
	waitFor(t, "media flows to close", func() bool { return hub.openFlows() == 0 })
	hub.mu.Lock()
	opened := hub.opens[0]
	hub.mu.Unlock()
	if opened != 0 {
		t.Fatal("a late answer opened a media flow on a device with calls switched off")
	}
	if inviter.sink.count(callmedia.FrameAudio) != 0 {
		t.Fatal("media was delivered on a device with calls switched off")
	}
}

func TestCallFrameBudget(t *testing.T) {
	var budget callFrameBudget
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < callFrameBurst; i++ {
		if !budget.allow(now) {
			t.Fatalf("frame %d of the initial burst was refused", i+1)
		}
	}
	if budget.allow(now) {
		t.Fatal("a frame beyond the burst was allowed")
	}
	now = now.Add(time.Second)
	allowed := 0
	for i := 0; i < 100; i++ {
		if budget.allow(now) {
			allowed++
		}
	}
	if allowed != callFramesPerSecond {
		t.Fatalf("one second refilled %d frames, want %d", allowed, callFramesPerSecond)
	}
	now = now.Add(time.Hour)
	allowed = 0
	for i := 0; i < 100; i++ {
		if budget.allow(now) {
			allowed++
		}
	}
	if allowed != callFrameBurst {
		t.Fatalf("a long idle period allowed %d frames, want the burst of %d", allowed, callFrameBurst)
	}
}

// A peer that floods call frames is not making a call. The session ends like
// any other protocol violation and the saved contact simply reconnects.
func TestCallFrameFloodEndsTheSessionAndTheContactReconnects(t *testing.T) {
	inviter, joiner, id, _ := connectedCallPair(t, 1, 1, nil)
	// Make sure the call path is installed on both ends first.
	first := establish(t, inviter, joiner, id, false, 0)
	if _, err := inviter.registry.EndCall(background(), id, first); err != nil {
		t.Fatal(err)
	}
	waitCall(t, joiner, id, "ended", isState(call.Idle))
	victim, err := inviter.registry.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	attacker, err := joiner.registry.manager(id)
	if err != nil {
		t.Fatal(err)
	}
	victim.mu.Lock()
	sessions := victim.authenticated
	victim.mu.Unlock()
	attacker.mu.Lock()
	session := attacker.controls.session
	attacker.mu.Unlock()
	var stranger call.ID
	stranger[0] = 0xEE
	for i := 0; i < 20*callFrameBurst; i++ {
		// Frames for a call that does not exist: harmless one at a time.
		if writePacket(session, packet{kind: callPacket, call: call.Frame{Kind: call.KindKeyframeRequest, Call: stranger}}) != nil {
			break // the victim has already closed the session
		}
	}
	waitFor(t, "the flooded session to be replaced", func() bool {
		victim.mu.Lock()
		defer victim.mu.Unlock()
		return victim.authenticated > sessions && victim.sessionActive
	})
	// Nothing durable was lost, and calls work on the new session.
	if _, err := inviter.registry.Send(id, "still here"); err != nil {
		t.Fatal(err)
	}
	waitRegistry(t, joiner.registryPeer, id, func(s Snapshot) bool { return len(s.Messages) == 1 && s.Messages[0].Text == "still here" })
	time.Sleep(call.DefaultMinOfferGap)
	establish(t, inviter, joiner, id, false, 0)
}
