package messaging

import (
	"context"
	"net"
	"sync"
	"time"

	"whatsup/internal/call"
	"whatsup/internal/callmedia"
	"whatsup/internal/sessionauth"
)

// callPacket carries one bounded call signaling frame on the authenticated
// session. It exists only when both peers negotiated the call protocol.
const callPacket byte = 9

// CallSink receives the active call's media and peer requests for delivery to
// the interface. Every method must return quickly and must not call back into
// a Manager. Nothing here is persisted or logged.
type CallSink interface {
	// CallFrame delivers one authenticated, complete encoded frame.
	CallFrame(contactID string, id call.ID, frame callmedia.Frame)
	// CallKeyframeNeeded reports that the peer cannot decode our video until
	// it gets a fresh keyframe.
	CallKeyframeNeeded(contactID string, id call.ID)
	// CallReport is the peer's view of how well our media is arriving.
	CallReport(contactID string, id call.ID, report call.Report)
}

// CallOptions is backend-only call wiring. The zero value keeps calls off:
// every offer is refused as unavailable and none can be placed.
type CallOptions struct {
	// Policy reports the current local settings each time a call is placed,
	// offered or answered. Nil means voice and video calls are both off.
	Policy func() (audio, video bool)
	// Slot is shared by every contact on this device so that at most one call
	// exists at a time. Nil gives the contact a private slot.
	Slot *CallSlot
	// Sink may be nil, in which case received media is discarded.
	Sink CallSink
	// StallTimeout ends a call whose media has been silent this long. Zero
	// selects DefaultCallStallTimeout.
	StallTimeout time.Duration
	// RingTimeout and ConnectTimeout override the call package defaults when
	// positive. They exist for tests; the application leaves them zero.
	RingTimeout    time.Duration
	ConnectTimeout time.Duration
}

// DefaultCallStallTimeout is how long authenticated media may be absent before
// the call is ended as failed.
const DefaultCallStallTimeout = 30 * time.Second

// CallSlot lets one device hold at most one call across all of its contacts.
type CallSlot struct {
	mu     sync.Mutex
	holder string
}

func NewCallSlot() *CallSlot { return &CallSlot{} }

func (s *CallSlot) acquire(contactID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holder != "" && s.holder != contactID {
		return false
	}
	s.holder = contactID
	return true
}

func (s *CallSlot) release(contactID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.holder == contactID {
		s.holder = ""
	}
}

type callRequestKind uint8

const (
	callStart callRequestKind = iota + 1
	callAnswer
	callEnd
	callSetMuted
	callSetCamera
	callEnforce
)

// Inbound call frames are tiny and rare: a handful to set a call up, then
// about one reception report a second and an occasional keyframe request. A
// peer sending them faster than this is not making a call. The session is
// ended, as for any other protocol violation; saved pins, history and the
// outbox are untouched and the normal reconnect follows.
const (
	callFrameBurst      = 40
	callFramesPerSecond = 20
)

type callFrameBudget struct {
	tokens float64
	at     time.Time
}

func (b *callFrameBudget) allow(now time.Time) bool {
	if b.at.IsZero() {
		b.tokens = callFrameBurst
	} else if now.After(b.at) {
		if b.tokens += now.Sub(b.at).Seconds() * callFramesPerSecond; b.tokens > callFrameBurst {
			b.tokens = callFrameBurst
		}
	}
	b.at = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type callResult struct {
	snapshot call.Snapshot
	err      error
}

type callRequest struct {
	kind               callRequestKind
	id                 call.ID
	video              bool
	codecs             call.Codec
	micMuted, cameraOn bool
	done               chan callResult
}

// callSession is the request path into one authenticated session's loop. A
// request never survives the session it was posted to: calls are not queued.
type callSession struct {
	requests chan *callRequest
	closed   chan struct{}
}

type callState struct {
	options CallOptions
	slot    *CallSlot
	// machine is touched only by the exchange goroutine of the live session.
	// Sessions are serialized by the route claim, and every session ends by
	// returning the machine to idle.
	machine *call.Machine
	// The fields below are guarded by the manager mutex.
	view    call.Snapshot
	session *callSession
	media   *callMediaRun
}

// Call returns the display state of this contact's call slot.
func (m *Manager) Call() call.Snapshot {
	if m == nil || m.managerState == nil {
		return call.Snapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls.view
}

func (m *Manager) callDo(ctx context.Context, request *callRequest) (call.Snapshot, error) {
	if m == nil || m.managerState == nil || ctx == nil || ctx.Err() != nil {
		return call.Snapshot{}, ErrUnavailable
	}
	m.mu.Lock()
	live := m.calls.session
	blocked := m.closed || m.paused || m.removing || m.peerRemoved || !m.sessionActive || !m.capabilities.Calls || live == nil
	m.mu.Unlock()
	if blocked {
		return call.Snapshot{}, ErrUnavailable
	}
	request.done = make(chan callResult, 1)
	select {
	case live.requests <- request:
	case <-live.closed:
		return call.Snapshot{}, ErrUnavailable
	case <-ctx.Done():
		return call.Snapshot{}, ErrUnavailable
	}
	select {
	case result := <-request.done:
		return result.snapshot, result.err
	case <-live.closed:
		select {
		case result := <-request.done:
			return result.snapshot, result.err
		default:
			return call.Snapshot{}, ErrUnavailable
		}
	case <-ctx.Done():
		return call.Snapshot{}, ErrUnavailable
	}
}

// StartCall places a call on the live authenticated session. With no live
// session, an older peer, or calls switched off, it fails at once; a call is
// never queued for later.
func (m *Manager) StartCall(ctx context.Context, video bool, codecs call.Codec) (call.Snapshot, error) {
	return m.callDo(ctx, &callRequest{kind: callStart, video: video, codecs: codecs})
}

// AnswerCall accepts the ringing call, as voice or, if it was offered and is
// allowed here, as video.
func (m *Manager) AnswerCall(ctx context.Context, id call.ID, video bool, codecs call.Codec) (call.Snapshot, error) {
	return m.callDo(ctx, &callRequest{kind: callAnswer, id: id, video: video, codecs: codecs})
}

// EndCall cancels, declines or hangs up the given call, whichever applies.
func (m *Manager) EndCall(ctx context.Context, id call.ID) (call.Snapshot, error) {
	return m.callDo(ctx, &callRequest{kind: callEnd, id: id})
}

// CallSettingsChanged applies the current call settings to a call already in
// progress with this contact. Switching calls off ends it in any state, also
// while it is ringing or an answer is on its way; switching only video off
// ends a video call or turns a ringing one into a voice call. With no live
// session there is no call and nothing to do.
func (m *Manager) CallSettingsChanged(ctx context.Context) {
	_, _ = m.callDo(ctx, &callRequest{kind: callEnforce})
}

// SetCallMuted records and announces this side's mute state, and nothing
// else. The session loop owns the call's state: each control changes its own
// setting there, so two controls used at the same moment cannot overwrite one
// another with what they last saw.
func (m *Manager) SetCallMuted(ctx context.Context, id call.ID, muted bool) (call.Snapshot, error) {
	return m.callDo(ctx, &callRequest{kind: callSetMuted, id: id, micMuted: muted})
}

// SetCallCamera records and announces this side's camera state, and nothing
// else.
func (m *Manager) SetCallCamera(ctx context.Context, id call.ID, on bool) (call.Snapshot, error) {
	return m.callDo(ctx, &callRequest{kind: callSetCamera, id: id, cameraOn: on})
}

func (m *Manager) callMedia(id call.ID) *callMediaRun {
	if m == nil || m.managerState == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls.media == nil || m.calls.media.plan.Call != id {
		return nil
	}
	return m.calls.media
}

// SendCallAudio seals and sends one encoded audio frame of the given call.
func (m *Manager) SendCallAudio(id call.ID, frame []byte, timestamp uint32) error {
	run := m.callMedia(id)
	if run == nil {
		return ErrUnavailable
	}
	return run.sendAudio(frame, timestamp)
}

// SendCallVideo seals and sends one encoded video frame of the given video call.
func (m *Manager) SendCallVideo(id call.ID, frame []byte, timestamp uint32, key bool) error {
	run := m.callMedia(id)
	if run == nil || !run.plan.Video {
		return ErrUnavailable
	}
	return run.sendVideo(frame, timestamp, key)
}

// callRuntime is the call side of one authenticated session. Every method is
// called from that session's exchange goroutine, the only writer on the
// session, and is safe on a nil receiver for sessions without the capability.
type callRuntime struct {
	m       *Manager
	session *sessionauth.Session
	raw     net.Conn
	ctx     context.Context
	machine *call.Machine
	live    *callSession
	events  chan callMediaEvent
	run     *callMediaRun
	budget  callFrameBudget
}

func (m *Manager) newCallRuntime(ctx context.Context, session *sessionauth.Session) *callRuntime {
	if !session.Capabilities().Calls {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls.machine == nil {
		role := call.Joiner
		if m.record.Role == "inviter" {
			role = call.Inviter
		}
		machine, err := call.New(call.Config{Role: role, RingTimeout: m.calls.options.RingTimeout, ConnectTimeout: m.calls.options.ConnectTimeout})
		if err != nil {
			return nil
		}
		m.calls.machine = machine
	}
	if m.calls.slot == nil {
		if m.calls.slot = m.calls.options.Slot; m.calls.slot == nil {
			m.calls.slot = NewCallSlot()
		}
	}
	live := &callSession{requests: make(chan *callRequest, 4), closed: make(chan struct{})}
	m.calls.session = live
	return &callRuntime{m: m, session: session, raw: m.raw, ctx: ctx, machine: m.calls.machine, live: live, events: make(chan callMediaEvent, 16)}
}

func (r *callRuntime) requests() <-chan *callRequest {
	if r == nil {
		return nil
	}
	return r.live.requests
}

func (r *callRuntime) mediaEvents() <-chan callMediaEvent {
	if r == nil {
		return nil
	}
	return r.events
}

// env reads local policy now. claim also tries to take the device call slot,
// which is needed only when a new call could begin.
func (r *callRuntime) env(claim bool) call.Env {
	var env call.Env
	if policy := r.m.calls.options.Policy; policy != nil {
		env.AudioAllowed, env.VideoAllowed = policy()
	}
	if claim {
		env.DeviceBusy = !r.m.calls.slot.acquire(r.m.record.ID)
	}
	return env
}

func (r *callRuntime) stopMedia() {
	if r.run == nil {
		return
	}
	r.m.mu.Lock()
	if r.m.calls.media == r.run {
		r.m.calls.media = nil
	}
	r.m.mu.Unlock()
	r.run.stop()
	r.run = nil
}

// apply performs one input's effects. A failed write is a broken session and
// is returned so the exchange ends; media and the slot are settled regardless.
func (r *callRuntime) apply(effects call.Effects) error {
	var failed error
	for _, frame := range effects.Send {
		if failed == nil && writePacket(r.session, packet{kind: callPacket, call: frame}) != nil {
			failed = errProtocol
		}
	}
	if effects.CloseMedia || failed != nil {
		r.stopMedia()
	}
	if effects.OpenMedia != nil && failed == nil {
		r.stopMedia()
		stall := r.m.calls.options.StallTimeout
		if stall <= 0 {
			stall = DefaultCallStallTimeout
		}
		r.run = startCallMedia(r.ctx, r.session, r.raw, *effects.OpenMedia, r.m.record.Role == "inviter", r.m.record.ID, r.m.calls.options.Sink, r.events, stall)
		r.m.mu.Lock()
		r.m.calls.media = r.run
		r.m.mu.Unlock()
	}
	snapshot := r.machine.Snapshot()
	if sink := r.m.calls.options.Sink; sink != nil && snapshot.State != call.Idle {
		if effects.KeyframeRequested {
			sink.CallKeyframeNeeded(r.m.record.ID, snapshot.Call)
		}
		if effects.Report != nil {
			sink.CallReport(r.m.record.ID, snapshot.Call, *effects.Report)
		}
	}
	if snapshot.State == call.Idle {
		r.m.calls.slot.release(r.m.record.ID)
	}
	r.m.mu.Lock()
	r.m.calls.view = snapshot
	if effects.Changed && !r.m.closed {
		r.m.bumpLocked()
	}
	r.m.mu.Unlock()
	return failed
}

// handle applies one call frame received on the authenticated session.
func (r *callRuntime) handle(frame call.Frame) error {
	if r == nil {
		return errProtocol
	}
	now := time.Now()
	if !r.budget.allow(now) {
		return errProtocol
	}
	return r.apply(r.machine.Handle(now, frame, r.env(frame.Kind == call.KindOffer)))
}

// request performs one local action and answers its caller.
func (r *callRuntime) request(request *callRequest) error {
	if r == nil || request == nil {
		return nil
	}
	now := time.Now()
	var effects call.Effects
	var err error
	switch request.kind {
	case callStart:
		media := call.Audio
		if request.video {
			media = call.Video
		}
		effects, err = r.machine.Start(now, media, request.codecs, r.env(true))
	case callAnswer:
		media := call.Audio
		if request.video {
			media = call.Video
		}
		effects, err = r.machine.Accept(now, request.id, media, request.codecs, r.env(false))
	case callEnd:
		effects, err = r.machine.Hangup(now, request.id, call.EndHungUp)
	case callSetMuted:
		effects, err = r.machine.SetMuted(request.id, request.micMuted)
	case callSetCamera:
		effects, err = r.machine.SetCamera(request.id, request.cameraOn)
	case callEnforce:
		effects = r.machine.Enforce(now, r.env(false))
	default:
		err = ErrUnavailable
	}
	failed := r.apply(effects)
	request.done <- callResult{snapshot: r.machine.Snapshot(), err: err}
	return failed
}

// tick applies ring and connect deadlines. The exchange's retry ticker calls
// it; a quarter-second of slack on a 45-second ring is immaterial.
func (r *callRuntime) tick() error {
	if r == nil {
		return nil
	}
	deadline := r.machine.Deadline()
	if now := time.Now(); !deadline.IsZero() && !now.Before(deadline) {
		return r.apply(r.machine.Tick(now))
	}
	return nil
}

// mediaEvent applies one event from the media loop. An event from an earlier
// call carries that call's ID and is refused by the machine.
func (r *callRuntime) mediaEvent(event callMediaEvent) error {
	if r == nil {
		return nil
	}
	now := time.Now()
	var effects call.Effects
	switch {
	case event.failed:
		effects, _ = r.machine.Hangup(now, event.call, call.EndMediaFailed)
	case event.ready:
		effects, _ = r.machine.MediaReady(now, event.call)
	case event.keyframe:
		effects, _ = r.machine.RequestKeyframe(event.call)
	case event.report != nil:
		effects, _ = r.machine.SendReport(event.call, *event.report)
	}
	return r.apply(effects)
}

// close ends any call when the authenticated session ends, for whatever
// reason: network loss, lock, pause, forget or quit. It returns only after the
// media flow is closed and its goroutines have finished.
func (r *callRuntime) close() {
	if r == nil {
		return
	}
	effects := r.machine.SessionLost(time.Now())
	r.stopMedia()
	r.m.calls.slot.release(r.m.record.ID)
	r.m.mu.Lock()
	r.m.calls.view = r.machine.Snapshot()
	if r.m.calls.session == r.live {
		r.m.calls.session = nil
	}
	if effects.Changed && !r.m.closed {
		r.m.bumpLocked()
	}
	r.m.mu.Unlock()
	close(r.live.closed)
}

// The registry forwards call actions to the one contact they name. An unknown,
// retired or transitioning contact has no call.

func (r *Registry) Call(id string) call.Snapshot {
	m, err := r.manager(id)
	if err != nil {
		return call.Snapshot{}
	}
	return m.Call()
}

func (r *Registry) StartCall(ctx context.Context, id string, video bool, codecs call.Codec) (call.Snapshot, error) {
	m, err := r.manager(id)
	if err != nil {
		return call.Snapshot{}, err
	}
	return m.StartCall(ctx, video, codecs)
}

func (r *Registry) AnswerCall(ctx context.Context, id string, callID call.ID, video bool, codecs call.Codec) (call.Snapshot, error) {
	m, err := r.manager(id)
	if err != nil {
		return call.Snapshot{}, err
	}
	return m.AnswerCall(ctx, callID, video, codecs)
}

func (r *Registry) EndCall(ctx context.Context, id string, callID call.ID) (call.Snapshot, error) {
	m, err := r.manager(id)
	if err != nil {
		return call.Snapshot{}, err
	}
	return m.EndCall(ctx, callID)
}

// CallSettingsChanged applies the current call settings to every contact's
// call. The desktop layer calls it whenever a call setting changes.
func (r *Registry) CallSettingsChanged(ctx context.Context) {
	if r == nil || r.registryState == nil {
		return
	}
	r.mu.Lock()
	managers := make([]*Manager, 0, len(r.managers))
	for _, m := range r.managers {
		managers = append(managers, m)
	}
	r.mu.Unlock()
	for _, m := range managers {
		m.CallSettingsChanged(ctx)
	}
}

func (r *Registry) SetCallMuted(ctx context.Context, id string, callID call.ID, muted bool) (call.Snapshot, error) {
	m, err := r.manager(id)
	if err != nil {
		return call.Snapshot{}, err
	}
	return m.SetCallMuted(ctx, callID, muted)
}

func (r *Registry) SetCallCamera(ctx context.Context, id string, callID call.ID, on bool) (call.Snapshot, error) {
	m, err := r.manager(id)
	if err != nil {
		return call.Snapshot{}, err
	}
	return m.SetCallCamera(ctx, callID, on)
}

func (r *Registry) SendCallAudio(id string, callID call.ID, frame []byte, timestamp uint32) error {
	m, err := r.manager(id)
	if err != nil {
		return err
	}
	return m.SendCallAudio(callID, frame, timestamp)
}

func (r *Registry) SendCallVideo(id string, callID call.ID, frame []byte, timestamp uint32, key bool) error {
	m, err := r.manager(id)
	if err != nil {
		return err
	}
	return m.SendCallVideo(callID, frame, timestamp, key)
}

// CallAvailability says whether a call could be placed with a contact now.
type CallAvailability struct {
	Connected  bool // a live authenticated session exists
	Supported  bool // and the peer negotiated the call protocol on it
	DeviceBusy bool // a call with a different contact holds this device
}

func (s *CallSlot) heldByOther(contactID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holder != "" && s.holder != contactID
}

func (m *Manager) CallAvailability() CallAvailability {
	if m == nil || m.managerState == nil {
		return CallAvailability{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	live := !m.closed && !m.paused && !m.removing && !m.peerRemoved && m.sessionActive
	result := CallAvailability{Connected: live, Supported: live && m.capabilities.Calls && m.calls.session != nil}
	slot := m.calls.slot
	if slot == nil {
		slot = m.calls.options.Slot
	}
	if slot != nil {
		result.DeviceBusy = slot.heldByOther(m.record.ID)
	}
	return result
}

func (r *Registry) CallAvailability(id string) CallAvailability {
	m, err := r.manager(id)
	if err != nil {
		return CallAvailability{}
	}
	return m.CallAvailability()
}
