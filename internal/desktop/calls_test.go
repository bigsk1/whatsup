package desktop

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"whatsup/internal/call"
	"whatsup/internal/callmedia"
	"whatsup/internal/messaging"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

// These tests drive two real desktop Services through the bound call methods
// with real pinned TLS, the real call state machine and real SRTP. The stream
// is a net.Pipe and the media flow an in-memory datagram link. They check the
// desktop layer's own promises: settings, the capture gate, the media stream
// attachment and teardown. They are not transport, device or webview evidence.

type callTestFlow struct {
	in, out chan []byte
	closed  chan struct{}
	once    sync.Once
	first   []byte
	limit   atomic.Int64
}

type callTestTimeout struct{}

func (callTestTimeout) Error() string   { return "i/o timeout" }
func (callTestTimeout) Timeout() bool   { return true }
func (callTestTimeout) Temporary() bool { return true }
func (callTestTimeout) Unwrap() error   { return os.ErrDeadlineExceeded }

func (f *callTestFlow) Read(p []byte) (int, error) {
	if f.first != nil {
		n := copy(p, f.first)
		f.first = nil
		return n, nil
	}
	var timer <-chan time.Time
	if at := f.limit.Load(); at != 0 {
		wait := time.Until(time.Unix(0, at))
		if wait <= 0 {
			return 0, callTestTimeout{}
		}
		t := time.NewTimer(wait)
		defer t.Stop()
		timer = t.C
	}
	select {
	case datagram := <-f.in:
		return copy(p, datagram), nil
	case <-f.closed:
		return 0, net.ErrClosed
	case <-timer:
		return 0, callTestTimeout{}
	}
}
func (f *callTestFlow) Write(p []byte) (int, error) {
	select {
	case <-f.closed:
		return 0, net.ErrClosed
	case f.out <- bytes.Clone(p):
	default:
	}
	return len(p), nil
}
func (f *callTestFlow) Close() error                     { f.once.Do(func() { close(f.closed) }); return nil }
func (f *callTestFlow) LocalAddr() net.Addr              { return nil }
func (f *callTestFlow) RemoteAddr() net.Addr             { return nil }
func (f *callTestFlow) SetDeadline(t time.Time) error    { return f.SetReadDeadline(t) }
func (f *callTestFlow) SetWriteDeadline(time.Time) error { return nil }
func (f *callTestFlow) SetReadDeadline(t time.Time) error {
	if t.IsZero() {
		f.limit.Store(0)
	} else {
		f.limit.Store(t.UnixNano())
	}
	return nil
}

type callTestConn struct {
	net.Conn
	towardMe, towardPeer chan []byte
	listener             bool
	mu                   sync.Mutex
	flow                 *callTestFlow
	opened               *atomic.Int32
	openDelay            *atomic.Int64
}

func (c *callTestConn) OpenCallMedia(ctx context.Context) (net.Conn, error) {
	// A real flow takes a moment to open: a tunnel dial on one side, a wait
	// for the first datagram on the other.
	if wait := time.Duration(c.openDelay.Load()); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, transport.ErrCallMedia
		}
	}
	flow := &callTestFlow{in: c.towardMe, out: c.towardPeer, closed: make(chan struct{})}
	if c.listener {
		select {
		case flow.first = <-c.towardMe:
		case <-ctx.Done():
			return nil, transport.ErrCallMedia
		}
	}
	c.mu.Lock()
	c.flow = flow
	c.mu.Unlock()
	c.opened.Add(1)
	return flow, nil
}

func (c *callTestConn) Close() error {
	c.mu.Lock()
	flow := c.flow
	c.mu.Unlock()
	if flow != nil {
		_ = flow.Close()
	}
	return c.Conn.Close()
}

type callTestListener struct {
	incoming chan net.Conn
	closed   chan struct{}
	once     sync.Once
}

func (l *callTestListener) Accept(ctx context.Context) (net.Conn, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.closed:
		return nil, net.ErrClosed
	case conn := <-l.incoming:
		return conn, nil
	}
}
func (l *callTestListener) Close() error { l.once.Do(func() { close(l.closed) }); return nil }

type callTestNetwork struct {
	mu       sync.Mutex
	listener *callTestListener
	flows    atomic.Int32
	// openDelay, in nanoseconds, slows every media flow opening.
	openDelay atomic.Int64
}

func (n *callTestNetwork) factories() messaging.Factories {
	return messaging.Factories{
		Profile: func(context.Context, string) (*transport.RelayProfile, error) { return nil, nil },
		Listen: func(context.Context, storage.PendingRecord, *transport.RelayProfile) (messaging.Listener, error) {
			l := &callTestListener{incoming: make(chan net.Conn), closed: make(chan struct{})}
			n.mu.Lock()
			n.listener = l
			n.mu.Unlock()
			return l, nil
		},
		Dial: func(ctx context.Context, _ storage.PendingRecord, _ *transport.RelayProfile) (net.Conn, error) {
			n.mu.Lock()
			l := n.listener
			n.mu.Unlock()
			if l == nil {
				return nil, messaging.ErrUnavailable
			}
			left, right := net.Pipe()
			a, b := make(chan []byte, 512), make(chan []byte, 512)
			select {
			case <-ctx.Done():
				left.Close()
				right.Close()
				return nil, ctx.Err()
			case <-l.closed:
				left.Close()
				right.Close()
				return nil, net.ErrClosed
			case l.incoming <- &callTestConn{Conn: left, towardMe: a, towardPeer: b, listener: true, opened: &n.flows, openDelay: &n.openDelay}:
				return &callTestConn{Conn: right, towardMe: b, towardPeer: a, opened: &n.flows, openDelay: &n.openDelay}, nil
			}
		},
	}
}

type callTestHost struct {
	mu      sync.Mutex
	capture []bool
	rings   int
	changes int
	service *Service
	capOpen atomic.Bool
}

func (h *callTestHost) options(network *callTestNetwork) RuntimeOptions {
	return RuntimeOptions{
		Factories: network.factories(),
		OnCallCapture: func(open bool) {
			h.mu.Lock()
			h.capture = append(h.capture, open)
			h.mu.Unlock()
			h.capOpen.Store(open)
		},
		OnIncomingCall: func() { h.mu.Lock(); h.rings++; h.mu.Unlock() },
		OnCallChanged:  func(CallHint) { h.mu.Lock(); h.changes++; h.mu.Unlock() },
	}
}

func (h *callTestHost) captureLog() []bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]bool(nil), h.capture...)
}

func (h *callTestHost) ringCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rings
}

// callTestPair returns two unlocked, connected desktop Services that share one
// approved contact, with voice calls set as requested on each.
func callTestPair(t *testing.T, voiceA, voiceB bool) (a, b *callTestHost, contact string, network *callTestNetwork) {
	t.Helper()
	network = &callTestNetwork{}
	build := func() *callTestHost {
		host := &callTestHost{}
		host.service = New(filepath.Join(t.TempDir(), "private"), &testKeys{})
		if err := ConfigureRuntime(host.service, host.options(network)); err != nil {
			t.Fatal(err)
		}
		if _, err := host.service.UnlockStorage(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = host.service.ServiceShutdown() })
		return host
	}
	a, b = build(), build()
	local, _ := a.service.application.Identity()
	remote, _ := b.service.application.Identity()
	node := tailcat.NewPrivateKey()
	node.Public.RegionID = 900
	client := key.NewNode()
	nodePrivate, _ := node.Private.MarshalText()
	clientPrivate, _ := client.MarshalText()
	record := storage.PendingRecord{Version: 2, ID: base64.RawURLEncoding.EncodeToString([]byte("synthetic-call-01")[:16]), Role: "inviter", Local: local.PublicKey(), Peer: remote.PublicKey(), LocalNodePrivate: string(nodePrivate), LocalNode: node.Private.Public().String(), PeerNode: client.Public().String(), PresharedKey: base64.RawURLEncoding.EncodeToString(node.Public.PresharedKey[:]), RelayServiceURL: disposableRelayURL, RelayFallbackConsent: true, ServerEndpoint: string(node.Public.Addr())}
	other := record
	other.Role = "joiner"
	other.Local, other.Peer = record.Peer, record.Local
	other.LocalNodePrivate, other.LocalNode, other.PeerNode = string(clientPrivate), record.PeerNode, record.LocalNode
	for _, item := range []struct {
		host   *callTestHost
		record storage.PendingRecord
		voice  bool
	}{{a, record, voiceA}, {b, other, voiceB}} {
		if item.host.service.application.SavePendingContact(item.record) != nil {
			t.Fatal("fixture pending save failed")
		}
		if _, err := item.host.service.application.CommitPendingContact(item.record); err != nil {
			t.Fatal(err)
		}
		// Reopen so the runtime is built by the Service itself, with its call wiring.
		item.host.service.LockStorage()
		if _, err := item.host.service.UnlockStorage(); err != nil {
			t.Fatal(err)
		}
		if item.voice {
			values, err := item.host.service.GetAppPreferences()
			if err != nil {
				t.Fatal(err)
			}
			values.VoiceCalls = true
			if _, err := item.host.service.SetAppPreferences(values); err != nil {
				t.Fatal(err)
			}
		}
	}
	contact = record.ID
	for _, host := range []*callTestHost{a, b} {
		if _, err := host.service.ConnectContact(contact); err != nil {
			t.Fatal(err)
		}
	}
	callWait(t, "both sides connected with a call-capable session", func() bool {
		va, _ := a.service.GetCall(contact)
		vb, _ := b.service.GetCall(contact)
		ready := func(v CallView, voice bool) bool {
			return (voice && v.Availability == "ready") || (!voice && v.Availability == "calls-off")
		}
		return ready(va, voiceA) && ready(vb, voiceB) &&
			a.service.messages.CallAvailability(contact).Supported && b.service.messages.CallAvailability(contact).Supported
	})
	return a, b, contact, network
}

func callWait(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(3 * time.Millisecond)
	}
}

func callState(t *testing.T, host *callTestHost, contact, want string) CallView {
	t.Helper()
	var view CallView
	callWait(t, "call state "+want, func() bool {
		view, _ = host.service.GetCall(contact)
		return view.State == want
	})
	return view
}

func TestVoiceCallsAreOffUntilSwitchedOnAndNeverRingADeviceThatHasThemOff(t *testing.T) {
	a, b, contact, network := callTestPair(t, true, false)
	if view, _ := b.service.GetCall(contact); view.Availability != "calls-off" {
		t.Fatalf("a fresh device reports call availability %q", view.Availability)
	}
	if _, err := b.service.StartCall(contact); err == nil || !strings.Contains(err.Error(), "switched off") {
		t.Fatalf("a call was placed with voice calls off: %v", err)
	}
	if _, err := a.service.StartCall(contact); err != nil {
		t.Fatalf("a call could not be placed with voice calls on: %v", err)
	}
	callWait(t, "the caller to learn the contact is unavailable", func() bool {
		view, _ := a.service.GetCall(contact)
		return view.State == "idle" && view.LastEnd == "peer-unavailable"
	})
	if view, _ := b.service.GetCall(contact); view.State != "idle" || view.CallID != "" || view.LastEnd != "" {
		t.Fatalf("an offer left a trace with calls off: %+v", view)
	}
	if b.ringCount() != 0 || len(b.captureLog()) != 0 || len(a.captureLog()) != 0 || network.flows.Load() != 0 {
		t.Fatal("a refused call rang, opened the capture gate or opened a media flow")
	}
}

// The review's first acceptance check: a ringing call alone never opens the
// microphone gate and cannot be given a media stream, on either side.
func TestARingingCallNeverOpensCaptureAndAnAnsweredCallDoesBeforeTheInterfaceKnows(t *testing.T) {
	a, b, contact, network := callTestPair(t, true, true)
	placed, err := a.service.StartCall(contact)
	if err != nil || placed.State != "outgoing" || placed.CallID == "" {
		t.Fatalf("call was not placed: %+v %v", placed, err)
	}
	ringing := callState(t, b, contact, "incoming")
	callWait(t, "the caller to see ringing", func() bool { v, _ := a.service.GetCall(contact); return v.PeerRinging })
	callWait(t, "one incoming-call announcement", func() bool { return b.ringCount() == 1 })
	time.Sleep(150 * time.Millisecond) // let any wrong gate change show itself
	for name, host := range map[string]*callTestHost{"caller": a, "callee": b} {
		if len(host.captureLog()) != 0 {
			t.Fatalf("the capture gate changed on the %s while the call was only ringing", name)
		}
		if session, err := AttachCallMedia(host.service, placed.CallID); session != nil || !errors.Is(err, ErrCallMedia) {
			t.Fatalf("a media stream was attached on the %s while the call was only ringing", name)
		}
	}
	if network.flows.Load() != 0 || a.ringCount() != 0 {
		t.Fatal("a ringing call opened a media flow or rang the caller")
	}

	// Answering: the gate is open by the time AnswerCall has returned the view.
	answered, err := b.service.AnswerCall(contact, ringing.CallID)
	if err != nil || (answered.State != "connecting" && answered.State != "active") {
		t.Fatalf("answer failed: %+v %v", answered, err)
	}
	if !b.capOpen.Load() {
		t.Fatal("the callee was told the call was answered before its capture gate opened")
	}
	callState(t, a, contact, "active")
	callState(t, b, contact, "active")
	if !a.capOpen.Load() {
		t.Fatal("the caller's capture gate did not open for the answered call")
	}
	if b.ringCount() != 1 {
		t.Fatal("the call was announced more than once")
	}

	// Media through the attachment the native stream handler uses.
	fromA, err := AttachCallMedia(a.service, placed.CallID)
	if err != nil {
		t.Fatal("the caller could not attach a media stream to its answered call")
	}
	fromB, err := AttachCallMedia(b.service, placed.CallID)
	if err != nil {
		t.Fatal("the callee could not attach a media stream to its answered call")
	}
	for i := 1; i <= 10; i++ {
		if err := fromA.SendAudio(uint32(i)*callmedia.AudioFrameSamples, bytes.Repeat([]byte{byte(i)}, 80)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 10; i++ {
		select {
		case frame := <-fromB.Frames():
			if frame.Timestamp != uint32(i)*callmedia.AudioFrameSamples || !bytes.Equal(frame.Data, bytes.Repeat([]byte{byte(i)}, 80)) {
				t.Fatalf("audio frame %d reached the other interface damaged", i)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("audio frame %d never reached the other interface", i)
		}
	}
	if stray, err := AttachCallMedia(a.service, "AAAAAAAAAAAAAAAAAAAAAA"); stray != nil || err == nil {
		t.Fatal("a media stream was attached for a call that does not exist")
	}

	// Hang-up closes the gate and the stream on both sides.
	if _, err := a.service.EndCall(contact, placed.CallID); err != nil {
		t.Fatal(err)
	}
	if a.capOpen.Load() {
		t.Fatal("the caller's capture gate was still open when EndCall returned")
	}
	ended := callState(t, b, contact, "idle")
	if ended.LastEnd != "peer-hung-up" || !ended.LastConnected {
		t.Fatalf("the finished call was recorded as %+v", ended)
	}
	if b.capOpen.Load() {
		t.Fatal("the callee's capture gate stayed open after the call ended")
	}
	for name, session := range map[string]*CallMediaSession{"caller": fromA, "callee": fromB} {
		select {
		case <-session.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("the %s's media stream stayed attached after the call ended", name)
		}
		if session.SendAudio(1, []byte{1}) == nil {
			t.Fatalf("the %s could send audio after the call ended", name)
		}
	}
	if got := a.captureLog(); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("caller capture gate history %v, want open then closed", got)
	}
}

// The review's second acceptance check, on the Go side: Calls Off and Lock
// each end the call and close capture.
func TestCallsOffAndLockEachEndTheCallAndCloseCapture(t *testing.T) {
	for _, how := range []string{"calls off", "lock"} {
		a, b, contact, _ := callTestPair(t, true, true)
		placed, err := a.service.StartCall(contact)
		if err != nil {
			t.Fatal(err)
		}
		ringing := callState(t, b, contact, "incoming")
		if _, err := b.service.AnswerCall(contact, ringing.CallID); err != nil {
			t.Fatal(err)
		}
		callState(t, a, contact, "active")
		callState(t, b, contact, "active")
		session, err := AttachCallMedia(b.service, placed.CallID)
		if err != nil {
			t.Fatal(err)
		}
		switch how {
		case "calls off":
			values, _ := b.service.GetAppPreferences()
			values.VoiceCalls = false
			if _, err := b.service.SetAppPreferences(values); err != nil {
				t.Fatal(err)
			}
			if view, _ := b.service.GetCall(contact); view.State != "idle" || view.LastEnd != "interrupted" || view.Availability != "calls-off" {
				t.Fatalf("%s: call view after switching calls off: %+v", how, view)
			}
		case "lock":
			b.service.LockStorage()
		}
		if b.capOpen.Load() {
			t.Fatalf("%s returned with the capture gate still open", how)
		}
		select {
		case <-session.Done():
		default:
			t.Fatalf("%s returned with the media stream still attached", how)
		}
		if session.SendAudio(1, []byte{1}) == nil {
			t.Fatalf("audio could be sent after %s", how)
		}
		callWait(t, how+": the other side to see the call end", func() bool {
			view, _ := a.service.GetCall(contact)
			return view.State == "idle"
		})
		callWait(t, how+": the other side's capture gate to close", func() bool { return !a.capOpen.Load() })
	}
}

func TestCallActionsRejectStaleOrMalformedHandles(t *testing.T) {
	a, b, contact, _ := callTestPair(t, true, true)
	placed, err := a.service.StartCall(contact)
	if err != nil {
		t.Fatal(err)
	}
	callState(t, b, contact, "incoming")
	for _, handle := range []string{"", "not-a-call-id", "AAAAAAAAAAAAAAAAAAAAAA", strings.Repeat("A", 21) + "Q"} {
		if _, err := b.service.AnswerCall(contact, handle); err == nil {
			t.Errorf("answer accepted handle %q", handle)
		}
		if _, err := b.service.EndCall(contact, handle); err == nil {
			t.Errorf("end accepted handle %q", handle)
		}
	}
	if view, _ := b.service.GetCall(contact); view.State != "incoming" || view.CallID != placed.CallID {
		t.Fatal("a bad handle disturbed the ringing call")
	}
	if _, err := a.service.StartCall(contact); err == nil {
		t.Fatal("a second call was placed over the first")
	}
	if _, err := b.service.EndCall(contact, placed.CallID); err != nil {
		t.Fatal(err)
	}
	callWait(t, "the caller to see the decline", func() bool { v, _ := a.service.GetCall(contact); return v.LastEnd == "peer-declined" })
	a.service.LockStorage()
	if _, err := a.service.GetCall(contact); err == nil {
		t.Fatal("call state was readable while storage is locked")
	}
	if _, err := a.service.StartCall(contact); err == nil {
		t.Fatal("a call was placed while storage is locked")
	}
}

// A device may place several calls in a row. Each one must carry audio both
// ways through the attachment the native stream handler uses, attached the
// way the interface does it: as soon as a view says the call was answered.
func TestRepeatedCallsFromTheSameSideEachCarryAudioBothWays(t *testing.T) {
	a, b, contact, _ := callTestPair(t, true, true)
	answered := func(v CallView) bool { return v.State == "connecting" || v.State == "active" }
	round := 0
	oneCall := func(caller, callee *callTestHost, callerHangsUp bool) {
		t.Helper()
		round++
		var placed CallView
		callWait(t, "the next call to be placed", func() bool {
			var err error
			placed, err = caller.service.StartCall(contact)
			return err == nil && placed.State == "outgoing"
		})
		ringing := callState(t, callee, contact, "incoming")
		if ringing.CallID != placed.CallID {
			t.Fatalf("call %d rang with a different ID", round)
		}
		view, err := callee.service.AnswerCall(contact, ringing.CallID)
		if err != nil || !answered(view) {
			t.Fatalf("call %d: answer failed: %+v %v", round, view, err)
		}
		fromCallee, err := AttachCallMedia(callee.service, placed.CallID)
		if err != nil {
			t.Fatalf("call %d: the callee could not attach media", round)
		}
		callWait(t, "the caller to see the answer", func() bool { v, _ := caller.service.GetCall(contact); return answered(v) })
		fromCaller, err := AttachCallMedia(caller.service, placed.CallID)
		if err != nil {
			t.Fatalf("call %d: the caller could not attach media", round)
		}
		callState(t, caller, contact, "active")
		callState(t, callee, contact, "active")
		for _, way := range []struct {
			name     string
			from, to *CallMediaSession
		}{{"caller to callee", fromCaller, fromCallee}, {"callee to caller", fromCallee, fromCaller}} {
			for i := 1; i <= 10; i++ {
				if err := way.from.SendAudio(uint32(i)*callmedia.AudioFrameSamples, bytes.Repeat([]byte{byte(round), byte(i)}, 40)); err != nil {
					t.Fatalf("call %d, %s: frame %d was refused: %v", round, way.name, i, err)
				}
			}
			for i := 1; i <= 10; i++ {
				select {
				case frame := <-way.to.Frames():
					if !bytes.Equal(frame.Data, bytes.Repeat([]byte{byte(round), byte(i)}, 40)) {
						t.Fatalf("call %d, %s: frame %d arrived damaged", round, way.name, i)
					}
				case <-time.After(3 * time.Second):
					t.Fatalf("call %d, %s: frame %d never arrived", round, way.name, i)
				}
			}
		}
		ender := callee
		if callerHangsUp {
			ender = caller
		}
		if _, err := ender.service.EndCall(contact, placed.CallID); err != nil {
			t.Fatalf("call %d: hang-up failed: %v", round, err)
		}
		callState(t, caller, contact, "idle")
		callState(t, callee, contact, "idle")
		for _, session := range []*CallMediaSession{fromCaller, fromCallee} {
			select {
			case <-session.Done():
			case <-time.After(2 * time.Second):
				t.Fatalf("call %d: a media stream stayed attached after the call", round)
			}
		}
		// A device refuses a new offer that follows the last one too closely.
		time.Sleep(call.DefaultMinOfferGap)
	}
	oneCall(a, b, true)
	oneCall(a, b, false)
	oneCall(b, a, true)
	oneCall(b, a, false)
}

// The interface starts sending microphone audio the moment it learns a call
// was answered, which can be before the encrypted media flow has opened. That
// early audio is dropped. It must not end the media stream: a stream closed
// here stays closed for the whole call, leaving it silent in both directions.
func TestAudioSentBeforeTheMediaFlowOpensDoesNotEndTheStream(t *testing.T) {
	for _, callerIsInviter := range []bool{true, false} {
		a, b, contact, network := callTestPair(t, true, true)
		caller, callee := a, b
		if !callerIsInviter {
			caller, callee = b, a
		}
		network.openDelay.Store(int64(400 * time.Millisecond))
		placed, err := caller.service.StartCall(contact)
		if err != nil {
			t.Fatal(err)
		}
		ringing := callState(t, callee, contact, "incoming")
		if _, err := callee.service.AnswerCall(contact, ringing.CallID); err != nil {
			t.Fatal(err)
		}
		fromCallee, err := AttachCallMedia(callee.service, placed.CallID)
		if err != nil {
			t.Fatal("the callee could not attach media to its answered call")
		}
		callWait(t, "the caller to see the answer", func() bool {
			v, _ := caller.service.GetCall(contact)
			return v.State == "connecting" || v.State == "active"
		})
		fromCaller, err := AttachCallMedia(caller.service, placed.CallID)
		if err != nil {
			t.Fatal("the caller could not attach media to its answered call")
		}
		// Both interfaces send at once, before either flow can be open.
		for name, session := range map[string]*CallMediaSession{"caller": fromCaller, "callee": fromCallee} {
			for i := 1; i <= 5; i++ {
				if err := session.SendAudio(uint32(i)*callmedia.AudioFrameSamples, []byte{1, 2, 3}); err != nil {
					t.Fatalf("inviter=%v: early audio from the %s ended its media stream: %v", callerIsInviter, name, err)
				}
			}
			select {
			case <-session.Done():
				t.Fatalf("inviter=%v: the %s's media stream closed while the call was still connecting", callerIsInviter, name)
			default:
			}
		}
		callState(t, caller, contact, "active")
		callState(t, callee, contact, "active")
		// The same streams carry audio once the flow is open.
		for _, way := range []struct{ from, to *CallMediaSession }{{fromCaller, fromCallee}, {fromCallee, fromCaller}} {
			for len(way.to.Frames()) > 0 {
				<-way.to.Frames()
			}
			if err := way.from.SendAudio(100*callmedia.AudioFrameSamples, []byte{9, 9, 9}); err != nil {
				t.Fatalf("inviter=%v: audio was refused on an active call: %v", callerIsInviter, err)
			}
			select {
			case frame := <-way.to.Frames():
				if !bytes.Equal(frame.Data, []byte{9, 9, 9}) {
					t.Fatalf("inviter=%v: audio arrived damaged", callerIsInviter)
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("inviter=%v: audio never arrived after the flow opened", callerIsInviter)
			}
		}
		// Once the call is over, sending is an error again.
		if _, err := caller.service.EndCall(contact, placed.CallID); err != nil {
			t.Fatal(err)
		}
		callState(t, callee, contact, "idle")
		if fromCaller.SendAudio(1, []byte{1}) == nil || fromCallee.SendAudio(1, []byte{1}) == nil {
			t.Fatalf("inviter=%v: audio was accepted after the call ended", callerIsInviter)
		}
	}
}
