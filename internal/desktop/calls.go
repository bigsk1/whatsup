package desktop

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"whatsup/internal/call"
	"whatsup/internal/callmedia"
	"whatsup/internal/messaging"
)

// CallView is the display state of one contact's call slot. It carries no
// key, address, path detail or media. The call ID is an opaque handle the
// interface passes back so an action cannot hit a different call.
type CallView struct {
	ContactID string `json:"contactID"`
	// State is idle, outgoing, incoming, connecting or active.
	State string `json:"state"`
	// CallID is empty when State is idle.
	CallID       string `json:"callID"`
	PeerRinging  bool   `json:"peerRinging"`
	MicMuted     bool   `json:"micMuted"`
	PeerMicMuted bool   `json:"peerMicMuted"`
	// ActiveSince is Unix milliseconds, or zero before media flows.
	ActiveSince int64 `json:"activeSince"`
	// Availability says why a call can or cannot be placed now: ready,
	// calls-off, offline, peer-update, busy or blocked. Blocked means this
	// device could not secure its microphone permission for this run.
	Availability string `json:"availability"`
	// LastEnd names how the most recent call with this contact finished, or
	// is empty. LastOutgoing and LastConnected describe that call.
	LastEnd       string `json:"lastEnd"`
	LastOutgoing  bool   `json:"lastOutgoing"`
	LastConnected bool   `json:"lastConnected"`
}

// CallChangedEvent is emitted with a CallHint when a contact's call view changes.
const CallChangedEvent = "whatsup:call-changed"

// CallHint tells the interface to fetch one contact's call view. It carries
// no state: a forged or stale hint can only cause a harmless read.
type CallHint struct {
	ContactID string `json:"contactID"`
}

const (
	callVoice = 1 << iota

	callActionTimeout = 3 * time.Second
	// callRevokedLimit bounds the memory of calls this device ended locally.
	callRevokedLimit = 16
	// callEndRetries and callEndRetryGap bound the background effort to tell
	// the session about a call that was already ended locally.
	callEndRetries  = 6
	callEndRetryGap = 2 * time.Second
	// callMediaQueue bounds frames waiting for the interface. At 50 audio
	// frames a second this is about five seconds; beyond it the oldest wait
	// is pointless, so new frames are dropped instead of growing memory.
	callMediaQueue = 256
)

// callControl is the desktop side of calling. Its settings word is the only
// source of call policy and defaults to everything off.
type callControl struct {
	settings atomic.Uint32
	// blocked is set, for the rest of the run, when the native host could not
	// secure the webview's capture permission. Nothing clears it.
	blocked atomic.Bool

	mu        sync.Mutex
	media     *CallMediaSession
	capturing bool
	views     map[string]CallView
	announced string
	// revoked holds the IDs of calls whose local side is over: the person
	// hung up, declined or switched calls off. Capture for such a call never
	// starts again, whatever the session still believes. revokedOrder keeps
	// insertion order so the set stays bounded.
	revoked      map[string]struct{}
	revokedOrder []string
	// stop is closed when storage locks or the service shuts down, which ends
	// any background effort started for a call of that unlocked period.
	stop chan struct{}
	// retryGap overrides callEndRetryGap when set. Tests shorten it.
	retryGap atomic.Int64
}

// stopSignal returns the channel that closes when the current unlocked period
// ends.
func (c *callControl) stopSignal() <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop == nil {
		c.stop = make(chan struct{})
	}
	return c.stop
}

// revoke ends a call locally. It reports whether the ID was new.
func (c *callControl) revoke(callID string) bool {
	if callID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, known := c.revoked[callID]; known {
		return false
	}
	if c.revoked == nil {
		c.revoked = map[string]struct{}{}
	}
	c.revoked[callID] = struct{}{}
	c.revokedOrder = append(c.revokedOrder, callID)
	if len(c.revokedOrder) > callRevokedLimit {
		delete(c.revoked, c.revokedOrder[0])
		c.revokedOrder = c.revokedOrder[1:]
	}
	return true
}

func (c *callControl) isRevoked(callID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, revoked := c.revoked[callID]
	return revoked
}

func (c *callControl) setSettings(values AppPreferences) {
	var word uint32
	if values.VoiceCalls {
		word |= callVoice
	}
	c.settings.Store(word)
}

// policy is read by the session loop at every offer, answer and start. Video
// is not offered in this phase whatever the stored settings say.
func (c *callControl) policy() (audio, video bool) {
	return c.settings.Load()&callVoice != 0 && !c.blocked.Load(), false
}

// BlockCallCapture makes calls unavailable for the rest of this run. The
// native host calls it when it could not confirm that the webview holds no
// saved device permission: without that, capture cannot be tied to a call. It
// is Go-only and is never bound to the interface.
func BlockCallCapture(s *Service) {
	if s == nil || s.calls.blocked.Swap(true) {
		return
	}
	s.applyCallSettings(AppPreferences{VoiceCalls: s.calls.settings.Load()&callVoice != 0})
}

// CallCaptureAllowed is the capture gate's single source of truth: whether,
// at this moment, an answered call that this device has not ended locally
// exists, with calls switched on and capture not blocked. The native host
// reads it when it applies the gate, so an update that runs late applies the
// present answer, never the one that was current when it was queued. It is
// Go-only and is never bound to the interface.
func CallCaptureAllowed(s *Service) bool {
	if s == nil {
		return false
	}
	if audio, _ := s.calls.policy(); !audio {
		return false
	}
	s.calls.mu.Lock()
	defer s.calls.mu.Unlock()
	return s.calls.capturing
}

type callSink struct{ control *callControl }

func (s callSink) CallFrame(_ string, id call.ID, frame callmedia.Frame) {
	if frame.Kind != callmedia.FrameAudio {
		return
	}
	s.control.mu.Lock()
	session := s.control.media
	s.control.mu.Unlock()
	if session == nil || session.id != id {
		return
	}
	select {
	case session.frames <- CallMediaFrame{Recovered: frame.Recovered, Timestamp: frame.Timestamp, Data: frame.Data}:
	default:
	}
}
func (callSink) CallKeyframeNeeded(string, call.ID)      {}
func (callSink) CallReport(string, call.ID, call.Report) {}

func (s *Service) callOptions() messaging.CallOptions {
	return messaging.CallOptions{Policy: s.calls.policy, Sink: callSink{&s.calls}}
}

func callStateName(state call.State) string {
	switch state {
	case call.Outgoing:
		return "outgoing"
	case call.Incoming:
		return "incoming"
	case call.Connecting:
		return "connecting"
	case call.Active:
		return "active"
	}
	return "idle"
}

func callEndName(end call.End) string {
	switch end {
	case call.EndHungUp:
		return "hung-up"
	case call.EndPeerHungUp:
		return "peer-hung-up"
	case call.EndCancelled:
		return "cancelled"
	case call.EndDeclined:
		return "declined"
	case call.EndPeerDeclined:
		return "peer-declined"
	case call.EndPeerBusy:
		return "peer-busy"
	case call.EndPeerUnavailable:
		return "peer-unavailable"
	case call.EndNoAnswer:
		return "no-answer"
	case call.EndMissed:
		return "missed"
	case call.EndConnectionLost:
		return "connection-lost"
	case call.EndMediaFailed:
		return "media-failed"
	case call.EndDeviceError:
		return "device-error"
	case call.EndProtocol:
		return "protocol"
	case call.EndInterrupted:
		return "interrupted"
	}
	return ""
}

// callView builds the public view and, before returning it, makes the native
// capture gate match it. The interface only learns that a call was answered
// from a view, so by the time it asks for the microphone the gate is open, and
// it is closed again before the interface learns the call is over.
func (s *Service) callView(messages *messaging.Registry, contactID string) CallView {
	snapshot := messages.Call(contactID)
	view := CallView{ContactID: contactID, State: callStateName(snapshot.State), PeerRinging: snapshot.PeerRinging,
		MicMuted: snapshot.MicMuted, PeerMicMuted: snapshot.PeerMicMuted,
		LastEnd: callEndName(snapshot.Last.End), LastOutgoing: snapshot.Last.Outgoing, LastConnected: snapshot.Last.Connected}
	if snapshot.State != call.Idle {
		view.CallID = snapshot.Call.String()
	}
	if !snapshot.ActiveSince.IsZero() {
		view.ActiveSince = snapshot.ActiveSince.UnixMilli()
	}
	audio, _ := s.calls.policy()
	availability := messages.CallAvailability(contactID)
	// A call this device ended locally is over for the interface and for
	// capture at once, even while the session has yet to hear about it. The
	// session still holds the slot until then, so another call cannot start.
	lingering := view.CallID != "" && s.calls.isRevoked(view.CallID)
	if lingering {
		view = CallView{ContactID: contactID, State: "idle", LastEnd: callEndName(call.EndHungUp),
			LastOutgoing: snapshot.Outgoing, LastConnected: snapshot.State == call.Active}
	}
	switch {
	case s.calls.blocked.Load():
		view.Availability = "blocked"
	case !audio:
		view.Availability = "calls-off"
	case lingering:
		view.Availability = "busy"
	case !availability.Connected:
		view.Availability = "offline"
	case !availability.Supported:
		view.Availability = "peer-update"
	case availability.DeviceBusy:
		view.Availability = "busy"
	default:
		view.Availability = "ready"
	}
	s.syncCallState(view)
	return view
}

// syncCallState records the view, opens or closes the capture gate, and
// reports transitions to the native host. It never takes Service.mu.
func (s *Service) syncCallState(view CallView) {
	c := &s.calls
	c.mu.Lock()
	if c.views == nil {
		c.views = map[string]CallView{}
	}
	previous, known := c.views[view.ContactID]
	c.views[view.ContactID] = view
	capturing := false
	for _, v := range c.views {
		capturing = capturing || v.State == "connecting" || v.State == "active"
	}
	gateChanged := capturing != c.capturing
	c.capturing = capturing
	ring := view.State == "incoming" && view.CallID != c.announced
	if ring {
		c.announced = view.CallID
	}
	var stale *CallMediaSession
	if !capturing && c.media != nil {
		stale, c.media = c.media, nil
	}
	c.mu.Unlock()
	if stale != nil {
		stale.Close()
	}
	options := s.callRuntime()
	if gateChanged && options.OnCallCapture != nil {
		// Synchronous on purpose: the gate is settled before the caller
		// returns the view that prompted it.
		options.OnCallCapture(capturing)
	}
	if ring && options.OnIncomingCall != nil {
		options.OnIncomingCall()
	}
	if (!known || previous != view) && options.OnCallChanged != nil {
		options.OnCallChanged(CallHint{ContactID: view.ContactID})
	}
}

func (s *Service) callRuntime() RuntimeOptions {
	s.callOptionsMu.Lock()
	defer s.callOptionsMu.Unlock()
	return s.callRuntimeOptions
}

// observeCall runs on the registry's notification goroutine for every contact
// change, before the general contact hint is passed on.
func (s *Service) observeCall(messages *messaging.Registry, contactID string) {
	if messages != nil && contactID != "" {
		s.callView(messages, contactID)
	}
}

// resetCalls closes capture and forgets every view when storage locks.
func (s *Service) resetCalls() {
	c := &s.calls
	c.mu.Lock()
	media, wasCapturing := c.media, c.capturing
	contacts := make([]string, 0, len(c.views))
	for id, view := range c.views {
		if view.State != "idle" {
			contacts = append(contacts, id)
		}
	}
	c.media, c.capturing, c.views, c.announced = nil, false, nil, ""
	c.revoked, c.revokedOrder = nil, nil
	if c.stop != nil {
		close(c.stop)
		c.stop = nil
	}
	c.mu.Unlock()
	if media != nil {
		media.Close()
	}
	options := s.callRuntime()
	if wasCapturing && options.OnCallCapture != nil {
		options.OnCallCapture(false)
	}
	if options.OnCallChanged != nil {
		for _, id := range contacts {
			options.OnCallChanged(CallHint{ContactID: id})
		}
	}
}

func (s *Service) callRegistry() (*messaging.Registry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return nil, errors.New("Unlock local storage before using calls")
	}
	return s.messages, nil
}

var errCallBlocked = errors.New("call capture is blocked")

func callError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, call.ErrBusy):
		return errors.New("You are already in a call")
	case errors.Is(err, errCallBlocked):
		return errors.New("Voice calls are unavailable until WhatsUp restarts: microphone access could not be secured")
	case errors.Is(err, call.ErrNotAllowed):
		return errors.New("Voice calls are switched off. Turn them on in Settings, App behavior")
	case errors.Is(err, call.ErrUnavailable):
		return errors.New("That call is no longer available")
	}
	return errors.New("This contact is not connected, or needs a newer WhatsUp, so a call cannot start")
}

// GetCall returns one contact's call view.
func (s *Service) GetCall(contactID string) (CallView, error) {
	messages, err := s.callRegistry()
	if err != nil {
		return CallView{}, err
	}
	return s.callView(messages, contactID), nil
}

// StartCall places a voice call. It never opens the microphone: capture starts
// in the interface only after the other person answers.
func (s *Service) StartCall(contactID string) (CallView, error) {
	messages, err := s.callRegistry()
	if err != nil {
		return CallView{}, err
	}
	if s.calls.blocked.Load() {
		return s.callView(messages, contactID), callError(errCallBlocked)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callActionTimeout)
	defer cancel()
	_, err = messages.StartCall(ctx, contactID, false, 0)
	return s.callView(messages, contactID), callError(err)
}

// AnswerCall accepts the ringing voice call.
func (s *Service) AnswerCall(contactID, callID string) (CallView, error) {
	messages, err := s.callRegistry()
	if err != nil {
		return CallView{}, err
	}
	id, ok := call.ParseID(callID)
	if !ok {
		return s.callView(messages, contactID), callError(call.ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callActionTimeout)
	defer cancel()
	_, err = messages.AnswerCall(ctx, contactID, id, false, 0)
	return s.callView(messages, contactID), callError(err)
}

// EndCall cancels, declines or hangs up the given call.
//
// The local side ends first and unconditionally: the call is revoked, which
// closes the capture gate and the media stream and shows the call as over,
// before the session is asked to tell the other device. If that request is
// slow or fails, this device stays ended and keeps trying in the background;
// the microphone never waits on the network.
func (s *Service) EndCall(contactID, callID string) (CallView, error) {
	messages, err := s.callRegistry()
	if err != nil {
		return CallView{}, err
	}
	id, ok := call.ParseID(callID)
	if !ok {
		return s.callView(messages, contactID), callError(call.ErrUnavailable)
	}
	if snapshot := messages.Call(contactID); snapshot.State == call.Idle || snapshot.Call != id {
		return s.callView(messages, contactID), callError(call.ErrUnavailable)
	}
	s.calls.revoke(callID)
	s.callView(messages, contactID)
	ctx, cancel := context.WithTimeout(context.Background(), callActionTimeout)
	defer cancel()
	if _, err := s.endCallRequest(ctx, messages, contactID, id); err != nil && !errors.Is(err, call.ErrUnavailable) {
		go s.finishEndingCall(messages, contactID, id)
	}
	return s.callView(messages, contactID), nil
}

func (s *Service) endCallRequest(ctx context.Context, messages *messaging.Registry, contactID string, id call.ID) (call.Snapshot, error) {
	if s.callEndForTest != nil {
		return s.callEndForTest(ctx, messages, contactID, id)
	}
	return messages.EndCall(ctx, contactID, id)
}

// finishEndingCall keeps asking the session to end a call that is already
// over locally, so the other device is told and the call slot is released. It
// gives up after a bounded effort, and at once when storage locks or the
// service shuts down; the session ending releases the slot too.
func (s *Service) finishEndingCall(messages *messaging.Registry, contactID string, id call.ID) {
	stop := s.calls.stopSignal()
	gap := time.Duration(s.calls.retryGap.Load())
	if gap <= 0 {
		gap = callEndRetryGap
	}
	for attempt := 0; attempt < callEndRetries; attempt++ {
		wait := time.NewTimer(gap)
		select {
		case <-stop:
			wait.Stop()
			return
		case <-wait.C:
		}
		if snapshot := messages.Call(contactID); snapshot.State == call.Idle || snapshot.Call != id {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), callActionTimeout)
		_, err := s.endCallRequest(ctx, messages, contactID, id)
		cancel()
		if err == nil || errors.Is(err, call.ErrUnavailable) {
			break
		}
	}
	s.callView(messages, contactID)
}

// SetCallMuted records and announces this side's mute state.
func (s *Service) SetCallMuted(contactID, callID string, muted bool) (CallView, error) {
	messages, err := s.callRegistry()
	if err != nil {
		return CallView{}, err
	}
	id, ok := call.ParseID(callID)
	if !ok {
		return s.callView(messages, contactID), callError(call.ErrUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), callActionTimeout)
	defer cancel()
	_, err = messages.SetCallMedia(ctx, contactID, id, muted, false)
	return s.callView(messages, contactID), callError(err)
}

// applyCallSettings makes a changed setting take effect on calls in progress.
//
// Switching calls off ends every call locally first, as EndCall does: capture
// is revoked and the views show the calls as over before the sessions are
// asked to enforce the setting, and it stays that way if they are slow.
func (s *Service) applyCallSettings(values AppPreferences) {
	s.calls.setSettings(values)
	s.mu.Lock()
	messages := s.messages
	s.mu.Unlock()
	if messages == nil {
		return
	}
	type ended struct {
		contactID string
		id        call.ID
	}
	var revoked []ended
	audio, _ := s.calls.policy()
	contacts, _ := messages.Contacts()
	if !audio {
		for _, contact := range contacts {
			snapshot := messages.Call(contact.ContactID)
			if snapshot.State == call.Idle {
				continue
			}
			s.calls.revoke(snapshot.Call.String())
			revoked = append(revoked, ended{contact.ContactID, snapshot.Call})
		}
		for _, contact := range contacts {
			s.callView(messages, contact.ContactID)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), callActionTimeout)
	defer cancel()
	s.enforceCallSettings(ctx, messages)
	for _, contact := range contacts {
		s.callView(messages, contact.ContactID)
	}
	for _, item := range revoked {
		if snapshot := messages.Call(item.contactID); snapshot.State != call.Idle && snapshot.Call == item.id {
			go s.finishEndingCall(messages, item.contactID, item.id)
		}
	}
}

func (s *Service) enforceCallSettings(ctx context.Context, messages *messaging.Registry) {
	if s.callEnforceForTest != nil {
		s.callEnforceForTest(ctx, messages)
		return
	}
	messages.CallSettingsChanged(ctx)
}

// CallMediaFrame is one encoded audio frame for the interface to decode.
type CallMediaFrame struct {
	Recovered bool
	Timestamp uint32
	Data      []byte
}

// CallMediaSession is the Go end of the interface's media stream for one
// answered call. It is Go-only: it is created by the native stream handler,
// never bound to the interface, and holds no key.
type CallMediaSession struct {
	service   *Service
	messages  *messaging.Registry
	contactID string
	id        call.ID
	frames    chan CallMediaFrame
	done      chan struct{}
	once      sync.Once
}

// ErrCallMedia reports that no answered call matches a media stream.
var ErrCallMedia = errors.New("call media is unavailable")

// AttachCallMedia binds a media stream to the answered call with this ID.
// A ringing call cannot be attached, so nothing can send or receive audio
// before the call is answered. A new attachment replaces an older one.
func AttachCallMedia(s *Service, callID string) (*CallMediaSession, error) {
	if s == nil {
		return nil, ErrCallMedia
	}
	id, ok := call.ParseID(callID)
	if !ok {
		return nil, ErrCallMedia
	}
	messages, err := s.callRegistry()
	if err != nil {
		return nil, ErrCallMedia
	}
	if audio, _ := s.calls.policy(); !audio {
		return nil, ErrCallMedia
	}
	if s.calls.isRevoked(callID) {
		return nil, ErrCallMedia
	}
	contacts, err := messages.Contacts()
	if err != nil {
		return nil, ErrCallMedia
	}
	for _, contact := range contacts {
		snapshot := messages.Call(contact.ContactID)
		if snapshot.Call != id || (snapshot.State != call.Connecting && snapshot.State != call.Active) {
			continue
		}
		session := &CallMediaSession{service: s, messages: messages, contactID: contact.ContactID, id: id,
			frames: make(chan CallMediaFrame, callMediaQueue), done: make(chan struct{})}
		s.calls.mu.Lock()
		previous := s.calls.media
		s.calls.media = session
		s.calls.mu.Unlock()
		if previous != nil {
			previous.Close()
		}
		return session, nil
	}
	return nil, ErrCallMedia
}

// Frames delivers received audio. It is never closed; select on Done.
func (m *CallMediaSession) Frames() <-chan CallMediaFrame { return m.frames }

// Done is closed when the session ends for any reason.
func (m *CallMediaSession) Done() <-chan struct{} { return m.done }

// SendAudio seals and sends one encoded audio frame from the interface. An
// error means the call is over and the stream should close.
//
// The interface starts capturing as soon as it learns the call was answered,
// and the encrypted media flow opens a moment later. A frame that arrives in
// between is dropped, never queued, and is not an error: closing the stream
// for it would leave the whole call silent in both directions.
func (m *CallMediaSession) SendAudio(timestamp uint32, frame []byte) error {
	select {
	case <-m.done:
		return ErrCallMedia
	default:
	}
	if audio, _ := m.service.calls.policy(); !audio {
		return ErrCallMedia
	}
	if m.service.calls.isRevoked(m.id.String()) {
		return ErrCallMedia
	}
	if m.messages.SendCallAudio(m.contactID, m.id, frame, timestamp) == nil {
		return nil
	}
	snapshot := m.messages.Call(m.contactID)
	if snapshot.Call == m.id && (snapshot.State == call.Connecting || snapshot.State == call.Active) {
		return nil
	}
	return ErrCallMedia
}

func (m *CallMediaSession) Close() {
	m.once.Do(func() {
		close(m.done)
		m.service.calls.mu.Lock()
		if m.service.calls.media == m {
			m.service.calls.media = nil
		}
		m.service.calls.mu.Unlock()
	})
}
