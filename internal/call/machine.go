package call

import (
	"crypto/rand"
	"errors"
	"time"
)

const (
	// DefaultRingTimeout bounds how long an unanswered call rings on either side.
	DefaultRingTimeout = 45 * time.Second
	// DefaultConnectTimeout bounds how long media may take to start after an answer.
	DefaultConnectTimeout = 15 * time.Second
	// DefaultMinOfferGap is the shortest accepted spacing between a peer's offers.
	DefaultMinOfferGap = 2 * time.Second

	controlBurst     = 20
	controlPerSecond = 10
)

var (
	// ErrUnavailable reports an action that does not apply to the current call.
	ErrUnavailable = errors.New("call action is not available")
	// ErrBusy reports that a call is already in progress on this device.
	ErrBusy = errors.New("a call is already in progress")
	// ErrNotAllowed reports that local settings do not permit this kind of call.
	ErrNotAllowed = errors.New("local settings do not allow this call")
)

// Role is this device's fixed pairing role for the contact. It only breaks the
// tie when both people call each other at the same moment.
type Role uint8

const (
	Inviter Role = iota + 1
	Joiner
)

// State is the single call slot for one contact.
type State uint8

const (
	Idle State = iota
	Outgoing
	Incoming
	Connecting
	Active
)

// End says how the most recent call finished, in local terms.
type End uint8

const (
	EndNone End = iota
	EndHungUp
	EndPeerHungUp
	EndCancelled
	EndDeclined
	EndPeerDeclined
	EndPeerBusy
	EndPeerUnavailable
	EndNoAnswer
	EndMissed
	EndConnectionLost
	EndMediaFailed
	EndDeviceError
	EndProtocol
	EndInterrupted
)

// Env is local policy at the moment of a decision. The machine never caches
// it: a setting switched off between two calls takes effect on the next one.
type Env struct {
	AudioAllowed bool // voice calls are enabled in settings
	VideoAllowed bool // video calls are enabled in settings
	DeviceBusy   bool // a call with a different contact holds this device
}

// Plan describes the media channel to open once a call is answered.
type Plan struct {
	Call ID
	// Answer is the answering side's random value. Call and Answer together
	// are the context media keys are derived under. Whichever side this
	// device is, one of the two values is its own fresh randomness, so the
	// peer cannot steer it into deriving keys it has used before.
	Answer ID
	Video  bool
	Codec  Codec
}

// KeyContext is the exact byte string media keys are bound to.
func (p Plan) KeyContext() [32]byte {
	var context [32]byte
	copy(context[:16], p.Call[:])
	copy(context[16:], p.Answer[:])
	return context
}

// Report is the far end's reception quality, for send-side adaptation.
type Report struct {
	Loss     uint8
	JitterMS uint16
}

// Effects is everything the caller must do after one input. Frames are sent in
// order on the authenticated session. OpenMedia and CloseMedia never both appear.
type Effects struct {
	Send              []Frame
	OpenMedia         *Plan
	CloseMedia        bool
	KeyframeRequested bool
	Report            *Report
	Changed           bool
}

// Result records the last finished call for display. It carries no content.
type Result struct {
	Call      ID
	Outgoing  bool
	Media     Media
	End       End
	Connected bool
	Duration  time.Duration
	At        time.Time
}

// Snapshot is the display state of the call slot.
type Snapshot struct {
	State        State
	Call         ID
	Outgoing     bool
	Media        Media
	Codec        Codec
	PeerRinging  bool
	MicMuted     bool
	CameraOn     bool
	PeerMicMuted bool
	PeerCameraOn bool
	Deadline     time.Time
	ActiveSince  time.Time
	Last         Result
}

// Config fixes the role and limits of one machine.
type Config struct {
	Role           Role
	RingTimeout    time.Duration
	ConnectTimeout time.Duration
	MinOfferGap    time.Duration
	// NewID supplies call IDs and answer values. It must return fresh random
	// nonzero values; nil selects the system random source.
	NewID func() (ID, error)
}

// Machine is not safe for concurrent use; its owner serializes every call.
type Machine struct {
	cfg Config

	state       State
	id          ID
	outgoing    bool
	media       Media // offered while ringing, agreed once answered
	localCodecs Codec
	peerCodecs  Codec
	codec       Codec
	peerRinging bool
	mediaOpen   bool
	deadline    time.Time
	activeSince time.Time

	micMuted, cameraOn         bool
	peerMicMuted, peerCameraOn bool

	lastOffer time.Time
	tokens    float64
	refilled  time.Time
	last      Result
	// recent holds the IDs of the latest calls this machine took part in or
	// refused, so a repeated offer does not ring again. It is a courtesy, not
	// the key-freshness guarantee: that comes from the answer value and from
	// the session refusing to export one key context twice.
	recent     [recentCalls]ID
	recentNext int
}

const recentCalls = 64

func (m *Machine) remember(id ID) {
	if id.IsZero() || m.seen(id) {
		return
	}
	m.recent[m.recentNext] = id
	m.recentNext = (m.recentNext + 1) % recentCalls
}

func (m *Machine) seen(id ID) bool {
	if id.IsZero() {
		return false
	}
	for _, known := range m.recent {
		if known == id {
			return true
		}
	}
	return false
}

// fresh returns a new random value that this machine has not used as a call ID.
func (m *Machine) fresh() (ID, bool) {
	id, err := m.cfg.NewID()
	return id, err == nil && !id.IsZero() && !m.seen(id)
}

func randomID() (ID, error) {
	var id ID
	for id.IsZero() {
		if _, err := rand.Read(id[:]); err != nil {
			return ID{}, err
		}
	}
	return id, nil
}

// New returns an idle machine. Zero durations select the defaults.
func New(cfg Config) (*Machine, error) {
	if cfg.Role != Inviter && cfg.Role != Joiner {
		return nil, ErrUnavailable
	}
	if cfg.RingTimeout <= 0 {
		cfg.RingTimeout = DefaultRingTimeout
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}
	if cfg.MinOfferGap <= 0 {
		cfg.MinOfferGap = DefaultMinOfferGap
	}
	if cfg.NewID == nil {
		cfg.NewID = randomID
	}
	return &Machine{cfg: cfg, tokens: controlBurst}, nil
}

// choose picks the best codec both sides have. VP8 is first because it is the
// one both supported webviews were measured to encode and decode.
func choose(local, peer Codec) Codec {
	common := local & peer & knownCodecs
	for _, candidate := range []Codec{VP8, VP9, H264} {
		if common&candidate != 0 {
			return candidate
		}
	}
	return 0
}

func (m *Machine) Snapshot() Snapshot {
	return Snapshot{State: m.state, Call: m.id, Outgoing: m.outgoing, Media: m.media, Codec: m.codec, PeerRinging: m.peerRinging,
		MicMuted: m.micMuted, CameraOn: m.cameraOn, PeerMicMuted: m.peerMicMuted, PeerCameraOn: m.peerCameraOn,
		Deadline: m.deadline, ActiveSince: m.activeSince, Last: m.last}
}

// Deadline is when Tick next has work, or zero when no timer is pending.
func (m *Machine) Deadline() time.Time { return m.deadline }

func (m *Machine) finish(now time.Time, end End, effects *Effects) {
	result := Result{Call: m.id, Outgoing: m.outgoing, Media: m.media, End: end, At: now}
	if m.state == Active {
		result.Connected = true
		if !m.activeSince.IsZero() && now.After(m.activeSince) {
			result.Duration = now.Sub(m.activeSince)
		}
	}
	if m.mediaOpen {
		effects.CloseMedia = true
		effects.OpenMedia = nil
	}
	m.remember(m.id)
	last, tokens, refilled, recent, next := m.lastOffer, m.tokens, m.refilled, m.recent, m.recentNext
	*m = Machine{cfg: m.cfg, last: result, lastOffer: last, tokens: tokens, refilled: refilled, recent: recent, recentNext: next}
	effects.Changed = true
}

func (m *Machine) answered(now time.Time, media Media, codec Codec, answer ID, effects *Effects) {
	m.state, m.media, m.codec = Connecting, media, codec
	m.deadline = now.Add(m.cfg.ConnectTimeout)
	m.mediaOpen = true
	m.micMuted, m.peerMicMuted = false, false
	// A camera is on only once its owner has said so. Answering a video call
	// agrees that video may be sent; it does not switch a camera on, and the
	// capture gate follows this state.
	m.cameraOn, m.peerCameraOn = false, false
	effects.OpenMedia = &Plan{Call: m.id, Answer: answer, Video: media == Video, Codec: codec}
	effects.Changed = true
}

// Start places a call. The offer is sent only on a live authenticated session;
// the caller checks that before calling and never queues the result.
func (m *Machine) Start(now time.Time, media Media, codecs Codec, env Env) (Effects, error) {
	if m.state != Idle || env.DeviceBusy {
		return Effects{}, ErrBusy
	}
	codecs &= knownCodecs
	if !media.valid() || !env.AudioAllowed || (media == Video && (!env.VideoAllowed || codecs == 0)) {
		return Effects{}, ErrNotAllowed
	}
	if media == Audio {
		codecs = 0
	}
	id, ok := m.fresh()
	if !ok {
		return Effects{}, ErrUnavailable
	}
	m.remember(id)
	m.state, m.id, m.outgoing, m.media, m.localCodecs = Outgoing, id, true, media, codecs
	m.deadline = now.Add(m.cfg.RingTimeout)
	return Effects{Send: []Frame{{Kind: KindOffer, Call: id, Media: media, Codecs: codecs}}, Changed: true}, nil
}

// Accept answers the ringing call. Asking for video on an audio offer, or with
// video switched off locally, is refused. With no codec in common the call is
// answered as audio.
func (m *Machine) Accept(now time.Time, id ID, media Media, codecs Codec, env Env) (Effects, error) {
	if m.state != Incoming || id != m.id {
		return Effects{}, ErrUnavailable
	}
	if !media.valid() || !env.AudioAllowed || (media == Video && (m.media != Video || !env.VideoAllowed)) {
		return Effects{}, ErrNotAllowed
	}
	var codec Codec
	if media == Video {
		if codec = choose(codecs, m.peerCodecs); codec == 0 {
			media = Audio
		}
	}
	answer, ok := m.fresh()
	if !ok {
		return Effects{}, ErrUnavailable
	}
	var effects Effects
	m.answered(now, media, codec, answer, &effects)
	effects.Send = []Frame{{Kind: KindAccept, Call: id, Media: media, Codecs: codec, Answer: answer}}
	return effects, nil
}

// Decline rejects the ringing call.
func (m *Machine) Decline(now time.Time, id ID) (Effects, error) {
	if m.state != Incoming || id != m.id {
		return Effects{}, ErrUnavailable
	}
	effects := Effects{Send: []Frame{{Kind: KindDecline, Call: id, Reason: ReasonDeclined}}}
	m.finish(now, EndDeclined, &effects)
	return effects, nil
}

// Hangup ends the current call from this side in any state. cause is one of
// EndHungUp, EndDeviceError, EndMediaFailed or EndInterrupted.
func (m *Machine) Hangup(now time.Time, id ID, cause End) (Effects, error) {
	if m.state == Idle || id != m.id {
		return Effects{}, ErrUnavailable
	}
	if cause != EndHungUp && cause != EndDeviceError && cause != EndMediaFailed && cause != EndInterrupted {
		return Effects{}, ErrUnavailable
	}
	var effects Effects
	switch m.state {
	case Outgoing:
		effects.Send = []Frame{{Kind: KindCancel, Call: id}}
		if cause == EndHungUp {
			cause = EndCancelled
		}
	case Incoming:
		reason := ReasonDeclined
		if cause == EndInterrupted {
			reason = ReasonUnavailable
		}
		effects.Send = []Frame{{Kind: KindDecline, Call: id, Reason: reason}}
		if cause == EndHungUp {
			cause = EndDeclined
		}
	default:
		reason := ReasonEnded
		switch cause {
		case EndDeviceError:
			reason = ReasonDeviceError
		case EndMediaFailed:
			reason = ReasonMediaFailed
		}
		effects.Send = []Frame{{Kind: KindHangup, Call: id, Reason: reason}}
	}
	m.finish(now, cause, &effects)
	return effects, nil
}

// MediaReady reports that authenticated media is flowing for the answered call.
func (m *Machine) MediaReady(now time.Time, id ID) (Effects, error) {
	if m.state != Connecting || id != m.id {
		return Effects{}, ErrUnavailable
	}
	m.state, m.activeSince, m.deadline = Active, now, time.Time{}
	return Effects{Changed: true}, nil
}

// SetMedia records this side's mute and camera state and tells the peer.
func (m *Machine) SetMedia(id ID, micMuted, cameraOn bool) (Effects, error) {
	if (m.state != Connecting && m.state != Active) || id != m.id {
		return Effects{}, ErrUnavailable
	}
	cameraOn = cameraOn && m.media == Video
	if micMuted == m.micMuted && cameraOn == m.cameraOn {
		return Effects{}, nil
	}
	m.micMuted, m.cameraOn = micMuted, cameraOn
	return Effects{Send: []Frame{{Kind: KindMediaState, Call: id, MicMuted: micMuted, CameraOn: cameraOn}}, Changed: true}, nil
}

// SetMuted changes only this side's microphone state. The camera stays as the
// machine has it, so a mute cannot carry an out-of-date camera state with it.
func (m *Machine) SetMuted(id ID, muted bool) (Effects, error) {
	return m.SetMedia(id, muted, m.cameraOn)
}

// SetCamera changes only this side's camera state. The microphone stays as
// the machine has it, so switching the camera can never undo a mute.
func (m *Machine) SetCamera(id ID, on bool) (Effects, error) {
	return m.SetMedia(id, m.micMuted, on)
}

// RequestKeyframe asks the peer's video encoder for a fresh keyframe.
func (m *Machine) RequestKeyframe(id ID) (Effects, error) {
	if (m.state != Connecting && m.state != Active) || id != m.id || m.media != Video {
		return Effects{}, ErrUnavailable
	}
	return Effects{Send: []Frame{{Kind: KindKeyframeRequest, Call: id}}}, nil
}

// SendReport tells the peer how well its media is arriving.
func (m *Machine) SendReport(id ID, report Report) (Effects, error) {
	if m.state != Active || id != m.id || report.Loss > 100 {
		return Effects{}, ErrUnavailable
	}
	return Effects{Send: []Frame{{Kind: KindReceiverReport, Call: id, Loss: report.Loss, JitterMS: report.JitterMS}}}, nil
}

// Tick applies an expired deadline. It is safe to call at any time.
func (m *Machine) Tick(now time.Time) Effects {
	var effects Effects
	if m.state == Idle || m.deadline.IsZero() || now.Before(m.deadline) {
		return effects
	}
	switch m.state {
	case Outgoing:
		effects.Send = []Frame{{Kind: KindCancel, Call: m.id}}
		m.finish(now, EndNoAnswer, &effects)
	case Incoming:
		// The caller's own timer cancels the call; nothing needs sending.
		m.finish(now, EndMissed, &effects)
	case Connecting:
		effects.Send = []Frame{{Kind: KindHangup, Call: m.id, Reason: ReasonMediaFailed}}
		m.finish(now, EndMediaFailed, &effects)
	}
	return effects
}

// Enforce applies a change of local settings to a call already in progress.
// With calls switched off, any call ends, in any state. With only video
// switched off, a ringing video call is presented as a voice call, and a video
// call that is being placed, connecting or active ends. The caller invokes it
// whenever a call setting changes; it is a no-op otherwise.
func (m *Machine) Enforce(now time.Time, env Env) Effects {
	if m.state == Idle || (env.AudioAllowed && (m.media != Video || env.VideoAllowed)) {
		return Effects{}
	}
	if env.AudioAllowed && m.state == Incoming {
		m.media = Audio
		return Effects{Changed: true}
	}
	effects, _ := m.Hangup(now, m.id, EndInterrupted)
	return effects
}

// SessionLost ends any call when the authenticated session ends. Nothing can
// be sent, and call keys die with the session.
func (m *Machine) SessionLost(now time.Time) Effects {
	var effects Effects
	if m.state != Idle {
		m.finish(now, EndConnectionLost, &effects)
	}
	return effects
}

func (m *Machine) allowControl(now time.Time) bool {
	if !m.refilled.IsZero() && now.After(m.refilled) {
		m.tokens += now.Sub(m.refilled).Seconds() * controlPerSecond
		if m.tokens > controlBurst {
			m.tokens = controlBurst
		}
	}
	m.refilled = now
	if m.tokens < 1 {
		return false
	}
	m.tokens--
	return true
}

func (m *Machine) violation(now time.Time, effects *Effects) {
	effects.Send = append(effects.Send, Frame{Kind: KindHangup, Call: m.id, Reason: ReasonProtocol})
	m.finish(now, EndProtocol, effects)
}

// Handle applies one frame received on the authenticated session. A frame that
// does not belong to the current call is ignored; a frame that contradicts the
// current call ends that call. Neither ends the session.
func (m *Machine) Handle(now time.Time, frame Frame, env Env) Effects {
	var effects Effects
	if frame.Validate() != nil {
		return effects
	}
	if frame.Kind == KindOffer {
		m.offered(now, frame, env, &effects)
		return effects
	}
	if m.state == Idle || frame.Call != m.id {
		return effects
	}
	switch frame.Kind {
	case KindRinging:
		if m.state == Outgoing && !m.peerRinging {
			m.peerRinging, effects.Changed = true, true
		}
	case KindAccept:
		if m.state != Outgoing {
			m.violation(now, &effects)
			return effects
		}
		validVideo := frame.Media == Video && m.media == Video && frame.Codecs&m.localCodecs == frame.Codecs
		if frame.Media != Audio && !validVideo {
			m.violation(now, &effects)
			return effects
		}
		// Settings are read again now, not remembered from when the call was
		// placed. If calls, or video for a video answer, were switched off
		// while it rang, the answer starts no media: the call is hung up.
		if !env.AudioAllowed || (frame.Media == Video && !env.VideoAllowed) {
			effects.Send = append(effects.Send, Frame{Kind: KindHangup, Call: m.id, Reason: ReasonEnded})
			m.finish(now, EndInterrupted, &effects)
			return effects
		}
		m.answered(now, frame.Media, frame.Codecs, frame.Answer, &effects)
	case KindDecline:
		if m.state != Outgoing {
			return effects
		}
		end := EndPeerDeclined
		switch frame.Reason {
		case ReasonBusy:
			end = EndPeerBusy
		case ReasonUnavailable:
			end = EndPeerUnavailable
		}
		m.finish(now, end, &effects)
	case KindCancel:
		// Only the caller cancels. Its cancel can cross this side's answer on
		// the wire: the caller is then already idle and will never send media,
		// so the answered call ends here at once instead of waiting out the
		// connect timeout with a media flow open.
		switch {
		case m.outgoing:
		case m.state == Incoming, m.state == Connecting:
			m.finish(now, EndMissed, &effects)
		case m.state == Active:
			m.finish(now, EndPeerHungUp, &effects)
		}
	case KindHangup:
		end := EndPeerHungUp
		switch {
		case frame.Reason == ReasonProtocol:
			end = EndProtocol
		case frame.Reason == ReasonMediaFailed:
			end = EndMediaFailed
		case m.state == Incoming:
			end = EndMissed
		}
		m.finish(now, end, &effects)
	case KindMediaState, KindKeyframeRequest, KindReceiverReport:
		if (m.state != Connecting && m.state != Active) || !m.allowControl(now) {
			return effects
		}
		switch frame.Kind {
		case KindMediaState:
			camera := frame.CameraOn && m.media == Video
			if frame.MicMuted != m.peerMicMuted || camera != m.peerCameraOn {
				m.peerMicMuted, m.peerCameraOn, effects.Changed = frame.MicMuted, camera, true
			}
		case KindKeyframeRequest:
			effects.KeyframeRequested = m.media == Video
		case KindReceiverReport:
			if m.state == Active {
				effects.Report = &Report{Loss: frame.Loss, JitterMS: frame.JitterMS}
			}
		}
	}
	return effects
}

func (m *Machine) offered(now time.Time, frame Frame, env Env, effects *Effects) {
	// A repeat of the current call's offer, or of a recent call's, is never a
	// new call. Key freshness does not depend on this memory; see Plan.Answer.
	if (m.state != Idle && frame.Call == m.id) || m.seen(frame.Call) {
		return
	}
	decline := func(reason Reason) {
		effects.Send = append(effects.Send, Frame{Kind: KindDecline, Call: frame.Call, Reason: reason})
	}
	tooSoon := !m.lastOffer.IsZero() && now.Sub(m.lastOffer) < m.cfg.MinOfferGap
	m.lastOffer = now
	switch m.state {
	case Idle:
		switch {
		case tooSoon || env.DeviceBusy:
			decline(ReasonBusy)
		case !env.AudioAllowed:
			// Calls are off here: refuse without ringing.
			decline(ReasonUnavailable)
		default:
			media := frame.Media
			if media == Video && !env.VideoAllowed {
				media = Audio // ring as a voice call
			}
			m.state, m.id, m.outgoing, m.media, m.peerCodecs = Incoming, frame.Call, false, media, frame.Codecs
			m.deadline = now.Add(m.cfg.RingTimeout)
			effects.Send = append(effects.Send, Frame{Kind: KindRinging, Call: frame.Call})
			effects.Changed = true
		}
	case Outgoing:
		// Both people called each other. The inviter's call stands; the joiner
		// drops its own and answers the inviter's, since it plainly wants to talk.
		if m.cfg.Role == Inviter {
			return
		}
		answer, ok := m.fresh()
		if !env.AudioAllowed || !ok {
			// Calls were switched off after this side started calling (or no
			// fresh answer value is available): cancel our own call and
			// refuse theirs. Nothing is answered and no media opens.
			effects.Send = append(effects.Send, Frame{Kind: KindCancel, Call: m.id})
			decline(ReasonUnavailable)
			m.finish(now, EndInterrupted, effects)
			return
		}
		media, codec := Audio, Codec(0)
		if m.media == Video && frame.Media == Video && env.VideoAllowed {
			if codec = choose(m.localCodecs, frame.Codecs); codec != 0 {
				media = Video
			}
		}
		m.remember(m.id) // our abandoned call ID is spent
		m.id, m.outgoing, m.peerCodecs, m.peerRinging = frame.Call, false, frame.Codecs, false
		m.answered(now, media, codec, answer, effects)
		effects.Send = append(effects.Send, Frame{Kind: KindAccept, Call: frame.Call, Media: media, Codecs: codec, Answer: answer})
	default:
		decline(ReasonBusy)
	}
}
