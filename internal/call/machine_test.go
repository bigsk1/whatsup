package call

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/rand"
	"testing"
	"time"
)

var (
	allowAll  = Env{AudioAllowed: true, VideoAllowed: true}
	audioOnly = Env{AudioAllowed: true}
	callsOff  = Env{}
	epoch     = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
)

type side struct {
	t       *testing.T
	machine *Machine
	env     Env
	opened  int
	closed  int
}

func newSide(t *testing.T, role Role, env Env) *side {
	t.Helper()
	// Deterministic, never-repeating values: a per-role prefix and a counter.
	var counter uint32
	machine, err := New(Config{Role: role, NewID: func() (ID, error) {
		counter++
		id := ID{0: byte(role), 1: 0xC0}
		binary.BigEndian.PutUint32(id[12:], counter)
		return id, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &side{t: t, machine: machine, env: env}
}

// note records media effects and enforces that a channel is never opened twice
// or closed without having been opened.
func (s *side) note(effects Effects) Effects {
	s.t.Helper()
	if effects.OpenMedia != nil && effects.CloseMedia {
		s.t.Fatal("one input both opened and closed media")
	}
	if effects.OpenMedia != nil {
		s.opened++
	}
	if effects.CloseMedia {
		s.closed++
	}
	if s.opened-s.closed < 0 || s.opened-s.closed > 1 {
		s.t.Fatalf("media channel accounting broke: opened %d, closed %d", s.opened, s.closed)
	}
	return effects
}

// deliver passes every frame through the real wire encoding to the other side
// and returns what that side wants to send back.
func deliver(t *testing.T, now time.Time, effects Effects, to *side) Effects {
	t.Helper()
	var reply Effects
	for _, frame := range effects.Send {
		wire, err := Encode(frame)
		if err != nil {
			t.Fatalf("machine produced an unencodable frame %+v", frame)
		}
		decoded, err := Read(bytes.NewReader(wire))
		if err != nil {
			t.Fatalf("machine produced an undecodable frame %+v", frame)
		}
		got := to.note(to.machine.Handle(now, decoded, to.env))
		reply.Send = append(reply.Send, got.Send...)
		if got.OpenMedia != nil {
			reply.OpenMedia = got.OpenMedia
		}
		reply.CloseMedia = reply.CloseMedia || got.CloseMedia
		reply.KeyframeRequested = reply.KeyframeRequested || got.KeyframeRequested
		if got.Report != nil {
			reply.Report = got.Report
		}
	}
	return reply
}

func mustStart(t *testing.T, s *side, now time.Time, media Media, codecs Codec) Effects {
	t.Helper()
	effects, err := s.machine.Start(now, media, codecs, s.env)
	if err != nil {
		t.Fatalf("start refused: %v", err)
	}
	return s.note(effects)
}

// ring places a call from caller to callee and returns the call ID once the
// callee is ringing and the caller knows it.
func ring(t *testing.T, caller, callee *side, media Media, codecs Codec) ID {
	t.Helper()
	offer := mustStart(t, caller, epoch, media, codecs)
	ringing := deliver(t, epoch, offer, callee)
	deliver(t, epoch, ringing, caller)
	id := caller.machine.Snapshot().Call
	if callee.machine.Snapshot().State != Incoming || callee.machine.Snapshot().Call != id || !caller.machine.Snapshot().PeerRinging {
		t.Fatal("callee is not ringing for the caller's call")
	}
	return id
}

func answer(t *testing.T, caller, callee *side, id ID, media Media, codecs Codec) (Plan, Plan) {
	t.Helper()
	accepted, err := callee.machine.Accept(epoch, id, media, codecs, callee.env)
	if err != nil {
		t.Fatalf("accept refused: %v", err)
	}
	callee.note(accepted)
	back := deliver(t, epoch, accepted, caller)
	if accepted.OpenMedia == nil || back.OpenMedia == nil {
		t.Fatal("answering did not ask both sides to open media")
	}
	return *accepted.OpenMedia, *back.OpenMedia
}

func TestVoiceCallFromRingToHangup(t *testing.T) {
	for _, callerRole := range []Role{Inviter, Joiner} {
		calleeRole := Joiner
		if callerRole == Joiner {
			calleeRole = Inviter
		}
		caller, callee := newSide(t, callerRole, allowAll), newSide(t, calleeRole, allowAll)
		id := ring(t, caller, callee, Audio, 0)
		mine, theirs := answer(t, caller, callee, id, Audio, 0)
		if mine != theirs || mine.Call != id || mine.Video || mine.Codec != 0 {
			t.Fatalf("the two sides disagree about the media plan: %+v / %+v", mine, theirs)
		}
		for _, s := range []*side{caller, callee} {
			if s.machine.Snapshot().State != Connecting || s.machine.Deadline().IsZero() {
				t.Fatal("answered call is not waiting for media under a deadline")
			}
			if _, err := s.machine.MediaReady(epoch.Add(time.Second), id); err != nil {
				t.Fatal(err)
			}
			if s.machine.Snapshot().State != Active || !s.machine.Deadline().IsZero() {
				t.Fatal("call with media is not active")
			}
		}
		end := epoch.Add(61 * time.Second)
		hung, err := caller.machine.Hangup(end, id, EndHungUp)
		if err != nil || len(hung.Send) != 1 || hung.Send[0].Kind != KindHangup || !hung.CloseMedia {
			t.Fatalf("hang-up did not send one hangup and close media: %+v %v", hung, err)
		}
		caller.note(hung)
		if got := deliver(t, end, hung, callee); !got.CloseMedia || len(got.Send) != 0 {
			t.Fatalf("peer hang-up was not applied quietly: %+v", got)
		}
		mineLast, theirLast := caller.machine.Snapshot().Last, callee.machine.Snapshot().Last
		if mineLast.End != EndHungUp || theirLast.End != EndPeerHungUp || !mineLast.Connected || !theirLast.Connected ||
			mineLast.Duration != time.Minute || !mineLast.Outgoing || theirLast.Outgoing {
			t.Fatalf("call results are wrong: %+v / %+v", mineLast, theirLast)
		}
		for _, s := range []*side{caller, callee} {
			if s.machine.Snapshot().State != Idle || s.opened != 1 || s.closed != 1 {
				t.Fatal("call slot or media channel was not released")
			}
		}
	}
}

func TestVideoNegotiation(t *testing.T) {
	t.Run("best common codec", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Video, VP9|H264)
		mine, theirs := answer(t, caller, callee, id, Video, VP8|VP9|H264)
		if mine != theirs || !mine.Video || mine.Codec != VP9 {
			t.Fatalf("expected VP9 on both sides, got %+v / %+v", mine, theirs)
		}
		if !caller.machine.Snapshot().CameraOn || !callee.machine.Snapshot().PeerCameraOn {
			t.Fatal("video call did not start with cameras marked on")
		}
	})
	t.Run("no common codec becomes a voice call", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Video, H264)
		mine, theirs := answer(t, caller, callee, id, Video, VP8)
		if mine != theirs || mine.Video || mine.Codec != 0 || caller.machine.Snapshot().Media != Audio {
			t.Fatalf("expected a voice call on both sides, got %+v / %+v", mine, theirs)
		}
	})
	t.Run("video switched off on the callee rings as voice", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, audioOnly)
		id := ring(t, caller, callee, Video, VP8)
		if callee.machine.Snapshot().Media != Audio {
			t.Fatal("a video offer was presented as video with video switched off")
		}
		if _, err := callee.machine.Accept(epoch, id, Video, VP8, callee.env); !errors.Is(err, ErrNotAllowed) {
			t.Fatal("video was accepted with video switched off")
		}
		mine, theirs := answer(t, caller, callee, id, Audio, 0)
		if mine != theirs || mine.Video {
			t.Fatal("voice answer to a video offer was not agreed on both sides")
		}
		if got, _ := caller.machine.SetMedia(id, false, true); len(got.Send) != 0 || caller.machine.Snapshot().CameraOn {
			t.Fatal("camera could be switched on in a voice call")
		}
	})
	t.Run("accepting voice for a video offer", func(t *testing.T) {
		caller, callee := newSide(t, Joiner, allowAll), newSide(t, Inviter, allowAll)
		id := ring(t, caller, callee, Video, VP8)
		mine, theirs := answer(t, caller, callee, id, Audio, 0)
		if mine != theirs || mine.Video {
			t.Fatal("accept-without-camera was not agreed on both sides")
		}
	})
}

func TestCallsThatNeverConnect(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Audio, 0)
		declined, err := callee.machine.Decline(epoch, id)
		if err != nil {
			t.Fatal(err)
		}
		deliver(t, epoch, callee.note(declined), caller)
		if caller.machine.Snapshot().Last.End != EndPeerDeclined || callee.machine.Snapshot().Last.End != EndDeclined {
			t.Fatal("decline was not recorded on both sides")
		}
	})
	t.Run("cancelled by the caller", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Audio, 0)
		cancelled, err := caller.machine.Hangup(epoch, id, EndHungUp)
		if err != nil || cancelled.Send[0].Kind != KindCancel {
			t.Fatal("cancelling a ringing call did not send cancel")
		}
		deliver(t, epoch, caller.note(cancelled), callee)
		if caller.machine.Snapshot().Last.End != EndCancelled || callee.machine.Snapshot().Last.End != EndMissed {
			t.Fatal("cancel was not recorded as cancelled / missed")
		}
	})
	t.Run("no answer", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		ring(t, caller, callee, Audio, 0)
		early := epoch.Add(DefaultRingTimeout - time.Millisecond)
		if got := caller.machine.Tick(early); got.Changed || len(got.Send) != 0 {
			t.Fatal("ring timeout fired early")
		}
		late := epoch.Add(DefaultRingTimeout)
		timedOut := caller.note(caller.machine.Tick(late))
		if len(timedOut.Send) != 1 || timedOut.Send[0].Kind != KindCancel || caller.machine.Snapshot().Last.End != EndNoAnswer {
			t.Fatal("unanswered call did not cancel itself")
		}
		// The callee's own timer also fires; both paths end as missed.
		if got := callee.note(callee.machine.Tick(late)); len(got.Send) != 0 || callee.machine.Snapshot().Last.End != EndMissed {
			t.Fatal("callee ring timeout did not end silently as missed")
		}
	})
	t.Run("media never starts", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Audio, 0)
		answer(t, caller, callee, id, Audio, 0)
		late := epoch.Add(DefaultConnectTimeout)
		failed := caller.note(caller.machine.Tick(late))
		if !failed.CloseMedia || len(failed.Send) != 1 || failed.Send[0].Reason != ReasonMediaFailed {
			t.Fatal("connect timeout did not hang up and close media")
		}
		if got := deliver(t, late, failed, callee); !got.CloseMedia || callee.machine.Snapshot().Last.End != EndMediaFailed {
			t.Fatal("peer was not told that media failed")
		}
	})
	t.Run("calls switched off", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, callsOff)
		reply := deliver(t, epoch, mustStart(t, caller, epoch, Audio, 0), callee)
		if callee.machine.Snapshot().State != Idle || len(reply.Send) != 1 || reply.Send[0].Reason != ReasonUnavailable {
			t.Fatal("an offer rang, or was not refused as unavailable, with calls switched off")
		}
		deliver(t, epoch, reply, caller)
		if caller.machine.Snapshot().Last.End != EndPeerUnavailable {
			t.Fatal("caller did not learn the contact is unavailable")
		}
	})
	t.Run("device busy with another contact", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, Env{AudioAllowed: true, DeviceBusy: true})
		reply := deliver(t, epoch, mustStart(t, caller, epoch, Audio, 0), callee)
		deliver(t, epoch, reply, caller)
		if callee.machine.Snapshot().State != Idle || caller.machine.Snapshot().Last.End != EndPeerBusy {
			t.Fatal("busy device did not refuse as busy")
		}
	})
}

func TestStartIsRefusedWithoutPermissionOrWhileBusy(t *testing.T) {
	s := newSide(t, Inviter, allowAll)
	for name, attempt := range map[string]func() (Effects, error){
		"calls off":           func() (Effects, error) { return s.machine.Start(epoch, Audio, 0, callsOff) },
		"video off":           func() (Effects, error) { return s.machine.Start(epoch, Video, VP8, audioOnly) },
		"video without codec": func() (Effects, error) { return s.machine.Start(epoch, Video, 0, allowAll) },
		"only unknown codec":  func() (Effects, error) { return s.machine.Start(epoch, Video, 64, allowAll) },
		"unknown media":       func() (Effects, error) { return s.machine.Start(epoch, 9, 0, allowAll) },
	} {
		if effects, err := attempt(); !errors.Is(err, ErrNotAllowed) || len(effects.Send) != 0 {
			t.Errorf("%s: start was not refused", name)
		}
	}
	if _, err := s.machine.Start(epoch, Audio, 0, Env{AudioAllowed: true, DeviceBusy: true}); !errors.Is(err, ErrBusy) {
		t.Error("start was allowed while another contact's call holds the device")
	}
	mustStart(t, s, epoch, Audio, 0)
	if _, err := s.machine.Start(epoch, Audio, 0, allowAll); !errors.Is(err, ErrBusy) {
		t.Error("a second call was started over the first")
	}
	if s.machine.Snapshot().State != Outgoing {
		t.Error("a refused start disturbed the existing call")
	}
}

func TestBothPeopleCallingAtOnceBecomesOneCall(t *testing.T) {
	inviter, joiner := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	fromInviter := mustStart(t, inviter, epoch, Video, VP8|VP9)
	fromJoiner := mustStart(t, joiner, epoch, Video, VP9|H264)
	inviterID := inviter.machine.Snapshot().Call

	// The joiner's offer reaches the inviter first and is ignored.
	if got := deliver(t, epoch, fromJoiner, inviter); len(got.Send) != 0 || inviter.machine.Snapshot().Call != inviterID {
		t.Fatal("the inviter did not keep its own call")
	}
	accepted := deliver(t, epoch, fromInviter, joiner)
	if joiner.machine.Snapshot().State != Connecting || joiner.machine.Snapshot().Call != inviterID || joiner.machine.Snapshot().Outgoing {
		t.Fatal("the joiner did not switch to the inviter's call")
	}
	if accepted.OpenMedia == nil || !accepted.OpenMedia.Video || accepted.OpenMedia.Codec != VP9 {
		t.Fatalf("the joiner did not answer with the shared codec: %+v", accepted.OpenMedia)
	}
	back := deliver(t, epoch, accepted, inviter)
	if back.OpenMedia == nil || *back.OpenMedia != *accepted.OpenMedia || inviter.machine.Snapshot().State != Connecting {
		t.Fatal("the two sides did not converge on one answered call")
	}
	if inviter.opened != 1 || joiner.opened != 1 {
		t.Fatal("simultaneous calls opened more than one media channel")
	}

	// One side wanted voice only: the single call is a voice call.
	inviter, joiner = newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	fromInviter = mustStart(t, inviter, epoch, Video, VP8)
	mustStart(t, joiner, epoch, Audio, 0)
	accepted = deliver(t, epoch, fromInviter, joiner)
	if accepted.OpenMedia == nil || accepted.OpenMedia.Video {
		t.Fatal("a voice caller was pulled into video by a simultaneous video call")
	}
	if back := deliver(t, epoch, accepted, inviter); back.OpenMedia == nil || back.OpenMedia.Video {
		t.Fatal("the inviter did not accept the voice answer")
	}
}

func TestSessionLossEndsAnyCallWithoutSending(t *testing.T) {
	for _, stage := range []string{"outgoing", "incoming", "connecting", "active"} {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		wantClose := false
		if stage == "outgoing" {
			mustStart(t, caller, epoch, Audio, 0)
		} else {
			id := ring(t, caller, callee, Audio, 0)
			if stage != "incoming" {
				answer(t, caller, callee, id, Audio, 0)
				wantClose = true
			}
			if stage == "active" {
				_, _ = caller.machine.MediaReady(epoch, id)
				_, _ = callee.machine.MediaReady(epoch, id)
			}
		}
		for _, s := range []*side{caller, callee} {
			wasIdle := s.machine.Snapshot().State == Idle
			got := s.note(s.machine.SessionLost(epoch.Add(time.Second)))
			if len(got.Send) != 0 {
				t.Fatalf("%s: frames were produced for a session that is gone", stage)
			}
			if wasIdle {
				continue
			}
			if got.CloseMedia != wantClose || s.machine.Snapshot().State != Idle || s.machine.Snapshot().Last.End != EndConnectionLost {
				t.Fatalf("%s: session loss was not applied: %+v", stage, got)
			}
		}
	}
}

func TestHostilePeerFrames(t *testing.T) {
	t.Run("accept from the wrong direction ends that call only", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Audio, 0)
		got := callee.note(callee.machine.Handle(epoch, Frame{Kind: KindAccept, Call: id, Media: Audio, Answer: testID(0x55)}, callee.env))
		if len(got.Send) != 1 || got.Send[0].Reason != ReasonProtocol || callee.machine.Snapshot().Last.End != EndProtocol || got.OpenMedia != nil {
			t.Fatalf("a caller answering its own call was tolerated: %+v", got)
		}
	})
	t.Run("accept may not upgrade or pick an unoffered codec", func(t *testing.T) {
		for name, forged := range map[string]Frame{
			"video for a voice offer": {Kind: KindAccept, Media: Video, Codecs: VP8, Answer: testID(0x55)},
			"codec never offered":     {Kind: KindAccept, Media: Video, Codecs: H264, Answer: testID(0x55)},
		} {
			caller := newSide(t, Inviter, allowAll)
			media, codecs := Audio, Codec(0)
			if name == "codec never offered" {
				media, codecs = Video, VP8
			}
			mustStart(t, caller, epoch, media, codecs)
			forged.Call = caller.machine.Snapshot().Call
			got := caller.note(caller.machine.Handle(epoch, forged, caller.env))
			if got.OpenMedia != nil || caller.machine.Snapshot().Last.End != EndProtocol || len(got.Send) != 1 {
				t.Errorf("%s: forged accept opened media or was tolerated", name)
			}
		}
	})
	t.Run("frames for other calls change nothing", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Audio, 0)
		answer(t, caller, callee, id, Audio, 0)
		before := callee.machine.Snapshot()
		stranger := testID(0xEE)
		for _, frame := range []Frame{
			{Kind: KindHangup, Call: stranger, Reason: ReasonEnded}, {Kind: KindCancel, Call: stranger},
			{Kind: KindAccept, Call: stranger, Media: Audio, Answer: testID(0x55)}, {Kind: KindDecline, Call: stranger, Reason: ReasonBusy},
			{Kind: KindRinging, Call: stranger}, {Kind: KindMediaState, Call: stranger, MicMuted: true},
			{Kind: KindKeyframeRequest, Call: stranger}, {Kind: KindReceiverReport, Call: stranger, Loss: 50},
		} {
			if got := callee.note(callee.machine.Handle(epoch, frame, callee.env)); got.Changed || len(got.Send) != 0 || got.CloseMedia || got.Report != nil {
				t.Fatalf("frame kind %d for another call had an effect", frame.Kind)
			}
		}
		if callee.machine.Snapshot() != before {
			t.Fatal("frames for another call changed the current call")
		}
	})
	t.Run("a second offer during a call is refused as busy", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Audio, 0)
		answer(t, caller, callee, id, Audio, 0)
		got := callee.note(callee.machine.Handle(epoch.Add(time.Minute), Frame{Kind: KindOffer, Call: testID(0xEE), Media: Audio}, callee.env))
		if len(got.Send) != 1 || got.Send[0].Reason != ReasonBusy || got.Send[0].Call != testID(0xEE) || callee.machine.Snapshot().Call != id || got.CloseMedia {
			t.Fatalf("a second offer disturbed the call: %+v", got)
		}
	})
	t.Run("repeated and replayed offers do not ring again", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Audio, 0)
		offer := Frame{Kind: KindOffer, Call: id, Media: Audio}
		if got := callee.note(callee.machine.Handle(epoch, offer, callee.env)); got.Changed || len(got.Send) != 0 {
			t.Fatal("a duplicate offer rang twice")
		}
		declined, _ := callee.machine.Decline(epoch, id)
		callee.note(declined)
		if got := callee.note(callee.machine.Handle(epoch.Add(time.Hour), offer, callee.env)); got.Changed || len(got.Send) != 0 || callee.machine.Snapshot().State != Idle {
			t.Fatal("the finished call's offer rang again")
		}
	})
	t.Run("offers in quick succession are refused, not rung", func(t *testing.T) {
		callee := newSide(t, Joiner, allowAll)
		rings := 0
		now := epoch
		for i := 0; i < 50; i++ {
			id := testID(byte(i + 1))
			got := callee.note(callee.machine.Handle(now, Frame{Kind: KindOffer, Call: id, Media: Audio}, callee.env))
			if callee.machine.Snapshot().State == Incoming {
				rings++
				cancelled := callee.note(callee.machine.Handle(now, Frame{Kind: KindCancel, Call: id}, callee.env))
				if !cancelled.Changed {
					t.Fatal("cancel did not stop the ring")
				}
			} else if len(got.Send) != 1 || got.Send[0].Reason != ReasonBusy {
				t.Fatal("a rate-limited offer was not refused as busy")
			}
			now = now.Add(100 * time.Millisecond)
		}
		if rings != 1 {
			t.Fatalf("50 offers in 5 seconds produced %d rings", rings)
		}
		if got := callee.note(callee.machine.Handle(now.Add(DefaultMinOfferGap), Frame{Kind: KindOffer, Call: testID(0xF0), Media: Audio}, callee.env)); !got.Changed {
			t.Fatal("a normally spaced offer was refused after the burst")
		}
	})
	t.Run("in-call control frames are bounded", func(t *testing.T) {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, Video, VP8)
		answer(t, caller, callee, id, Video, VP8)
		_, _ = callee.machine.MediaReady(epoch, id)
		applied := 0
		for i := 0; i < 1000; i++ {
			if got := callee.machine.Handle(epoch, Frame{Kind: KindKeyframeRequest, Call: id}, callee.env); got.KeyframeRequested {
				applied++
			}
		}
		if applied != controlBurst {
			t.Fatalf("1000 instant keyframe requests produced %d encoder resets, want the burst of %d", applied, controlBurst)
		}
		if got := callee.machine.Handle(epoch.Add(time.Second), Frame{Kind: KindReceiverReport, Call: id, Loss: 5, JitterMS: 30}, callee.env); got.Report == nil || *got.Report != (Report{Loss: 5, JitterMS: 30}) {
			t.Fatal("control frames did not resume after the burst drained")
		}
		if callee.machine.Snapshot().State != Active {
			t.Fatal("a control flood ended the call")
		}
	})
	t.Run("invalid frames are ignored by the machine", func(t *testing.T) {
		callee := newSide(t, Joiner, allowAll)
		if got := callee.machine.Handle(epoch, Frame{Kind: KindOffer, Call: testID(1), Media: 7}, callee.env); got.Changed || len(got.Send) != 0 {
			t.Fatal("an invalid offer had an effect")
		}
	})
}

func TestLocalActionsRequireTheCurrentCall(t *testing.T) {
	caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	id := ring(t, caller, callee, Audio, 0)
	wrong := testID(0xEE)
	if _, err := callee.machine.Accept(epoch, wrong, Audio, 0, callee.env); !errors.Is(err, ErrUnavailable) {
		t.Error("accept with a stale call ID was allowed")
	}
	if _, err := callee.machine.Decline(epoch, wrong); !errors.Is(err, ErrUnavailable) {
		t.Error("decline with a stale call ID was allowed")
	}
	if _, err := caller.machine.Hangup(epoch, wrong, EndHungUp); !errors.Is(err, ErrUnavailable) {
		t.Error("hang-up with a stale call ID was allowed")
	}
	if _, err := caller.machine.Hangup(epoch, id, EndPeerHungUp); !errors.Is(err, ErrUnavailable) {
		t.Error("hang-up accepted a cause that only the peer can produce")
	}
	if _, err := caller.machine.Accept(epoch, id, Audio, 0, caller.env); !errors.Is(err, ErrUnavailable) {
		t.Error("the caller could accept its own call")
	}
	if _, err := callee.machine.MediaReady(epoch, id); !errors.Is(err, ErrUnavailable) {
		t.Error("media was marked ready before the call was answered")
	}
	if _, err := callee.machine.Accept(epoch, id, Audio, 0, callsOff); !errors.Is(err, ErrNotAllowed) {
		t.Error("a call was accepted after calls were switched off")
	}
	interrupted, err := callee.machine.Hangup(epoch, id, EndInterrupted)
	if err != nil || interrupted.Send[0].Kind != KindDecline || interrupted.Send[0].Reason != ReasonUnavailable {
		t.Error("an interrupted ringing call did not decline as unavailable")
	}
}

func TestMuteAndCameraStateReachThePeer(t *testing.T) {
	caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	id := ring(t, caller, callee, Video, VP8)
	answer(t, caller, callee, id, Video, VP8)
	changed, err := caller.machine.SetMedia(id, true, false)
	if err != nil || len(changed.Send) != 1 {
		t.Fatal("mute and camera-off were not announced")
	}
	deliver(t, epoch, changed, callee)
	if snapshot := callee.machine.Snapshot(); !snapshot.PeerMicMuted || snapshot.PeerCameraOn {
		t.Fatal("peer mute and camera state did not arrive")
	}
	if again, _ := caller.machine.SetMedia(id, true, false); len(again.Send) != 0 {
		t.Fatal("an unchanged media state was announced again")
	}
	request, err := callee.machine.RequestKeyframe(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := deliver(t, epoch, request, caller); !got.KeyframeRequested {
		t.Fatal("keyframe request did not reach the sender")
	}
}

// Random interleavings of local actions, peer frames and timers must never
// leave media open in an idle slot, open it twice, or close it unopened.
func TestRandomSequencesKeepMediaAccountingConsistent(t *testing.T) {
	random := rand.New(rand.NewSource(20261120))
	for round := 0; round < 300; round++ {
		inviter, joiner := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		sides := []*side{inviter, joiner}
		now := epoch
		var inFlight [2][]Frame // frames travelling to side i
		for step := 0; step < 120; step++ {
			now = now.Add(time.Duration(random.Intn(4000)) * time.Millisecond)
			i := random.Intn(2)
			s, other := sides[i], 1-i
			id := s.machine.Snapshot().Call
			var effects Effects
			switch random.Intn(9) {
			case 0:
				effects, _ = s.machine.Start(now, Media(1+random.Intn(2)), Codec(random.Intn(8)), s.env)
			case 1:
				effects, _ = s.machine.Accept(now, id, Media(1+random.Intn(2)), Codec(random.Intn(8)), s.env)
			case 2:
				effects, _ = s.machine.Decline(now, id)
			case 3:
				effects, _ = s.machine.Hangup(now, id, []End{EndHungUp, EndDeviceError, EndMediaFailed, EndInterrupted}[random.Intn(4)])
			case 4:
				effects, _ = s.machine.MediaReady(now, id)
			case 5:
				effects = s.machine.Tick(now)
			case 6:
				effects, _ = s.machine.SetMedia(id, random.Intn(2) == 0, random.Intn(2) == 0)
			default:
				if len(inFlight[i]) > 0 {
					effects = s.machine.Handle(now, inFlight[i][0], s.env)
					inFlight[i] = inFlight[i][1:]
				}
			}
			s.note(effects)
			inFlight[other] = append(inFlight[other], effects.Send...)
			snapshot := s.machine.Snapshot()
			open := s.opened-s.closed == 1
			if open != (snapshot.State == Connecting || snapshot.State == Active) {
				t.Fatalf("round %d step %d: media open=%v in state %d", round, step, open, snapshot.State)
			}
			if snapshot.State == Idle && (!snapshot.Call.IsZero() || !snapshot.Deadline.IsZero()) {
				t.Fatalf("round %d step %d: idle slot kept call state", round, step)
			}
		}
		for _, s := range sides {
			s.note(s.machine.SessionLost(now))
			if s.opened != s.closed || s.machine.Snapshot().State != Idle {
				t.Fatalf("round %d: session loss left a call or media channel behind", round)
			}
		}
	}
}

// Media keys are bound to the call ID plus the answering side's answer value.
// Whatever the peer does, each call a side takes part in must have a key
// context that side has never had before, because packet counters restart.
func TestEveryAnsweredCallHasAKeyContextNeverSeenBefore(t *testing.T) {
	t.Run("ordinary calls", func(t *testing.T) {
		inviter, joiner := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		seen := map[[32]byte]bool{}
		for i := 0; i < 200; i++ {
			caller, callee := inviter, joiner
			if i%2 == 1 {
				caller, callee = joiner, inviter
			}
			now := epoch.Add(time.Duration(i+1) * DefaultMinOfferGap)
			offer, err := caller.machine.Start(now, Audio, 0, caller.env)
			if err != nil {
				t.Fatalf("call %d was not placed: %v", i, err)
			}
			deliver(t, now, deliver(t, now, caller.note(offer), callee), caller)
			id := caller.machine.Snapshot().Call
			accepted, err := callee.machine.Accept(now, id, Audio, 0, callee.env)
			if err != nil || accepted.OpenMedia == nil {
				t.Fatalf("call %d was not answered: %v", i, err)
			}
			back := deliver(t, now, callee.note(accepted), caller)
			if back.OpenMedia == nil {
				t.Fatalf("call %d: the caller did not open media", i)
			}
			mine, theirs := *back.OpenMedia, *accepted.OpenMedia
			if mine.KeyContext() != theirs.KeyContext() || mine.Answer.IsZero() || mine.Answer == mine.Call {
				t.Fatalf("call %d: the two sides disagree on the key context or the answer value is unusable", i)
			}
			if seen[mine.KeyContext()] {
				t.Fatalf("call %d reused an earlier call's key context", i)
			}
			seen[mine.KeyContext()] = true
			hung, _ := caller.machine.Hangup(now, id, EndHungUp)
			deliver(t, now, caller.note(hung), callee)
		}
	})

	t.Run("peer offers an old call ID again", func(t *testing.T) {
		callee := newSide(t, Joiner, allowAll)
		now := epoch
		offer := func(id ID) Effects {
			now = now.Add(DefaultMinOfferGap)
			return callee.note(callee.machine.Handle(now, Frame{Kind: KindOffer, Call: id, Media: Audio}, callee.env))
		}
		accept := func(id ID) Plan {
			effects, err := callee.machine.Accept(now, id, Audio, 0, callee.env)
			if err != nil || effects.OpenMedia == nil {
				t.Fatalf("accept refused: %v", err)
			}
			callee.note(effects)
			return *effects.OpenMedia
		}
		hangup := func(id ID) {
			callee.note(callee.machine.Handle(now, Frame{Kind: KindHangup, Call: id, Reason: ReasonEnded}, callee.env))
		}
		old := testID(0xA1)
		if got := offer(old); !got.Changed {
			t.Fatal("first offer did not ring")
		}
		first := accept(old)
		hangup(old)
		// While it is remembered, the old ID does not even ring.
		if got := offer(old); got.Changed || len(got.Send) != 0 || callee.machine.Snapshot().State != Idle {
			t.Fatal("a recent call ID rang again")
		}
		// Push it out of the bounded memory with many other calls.
		for i := 0; i < recentCalls+8; i++ {
			other := ID{0: 0xB0, 14: byte(i >> 8), 15: byte(i)}
			if got := offer(other); !got.Changed {
				t.Fatalf("offer %d did not ring", i)
			}
			callee.note(callee.machine.Handle(now, Frame{Kind: KindCancel, Call: other}, callee.env))
		}
		// Now the peer can make the old ID ring again, but answering it cannot
		// reproduce the old key context: the answer value is ours and new.
		if got := offer(old); !got.Changed || callee.machine.Snapshot().State != Incoming {
			t.Fatal("test did not get the old call ID past the recent-call memory")
		}
		second := accept(old)
		if second.Call != first.Call {
			t.Fatal("test did not reuse the call ID")
		}
		if second.Answer == first.Answer || second.KeyContext() == first.KeyContext() {
			t.Fatal("answering a re-offered call ID reproduced the earlier key context")
		}
	})

	t.Run("peer answers with the same value every time", func(t *testing.T) {
		caller := newSide(t, Inviter, allowAll)
		seen := map[[32]byte]bool{}
		now := epoch
		for i := 0; i < 50; i++ {
			mustStart(t, caller, now, Audio, 0)
			id := caller.machine.Snapshot().Call
			got := caller.note(caller.machine.Handle(now, Frame{Kind: KindAccept, Call: id, Media: Audio, Answer: testID(0x77)}, caller.env))
			if got.OpenMedia == nil {
				t.Fatal("accept did not open media")
			}
			if seen[got.OpenMedia.KeyContext()] {
				t.Fatal("a constant answer value made the caller repeat a key context")
			}
			seen[got.OpenMedia.KeyContext()] = true
			hung, _ := caller.machine.Hangup(now, id, EndHungUp)
			caller.note(hung)
		}
	})

	t.Run("a random source that repeats or fails places and answers nothing", func(t *testing.T) {
		values := []ID{testID(1), testID(1), {}, testID(2), testID(1), {}}
		var failing bool
		machine, err := New(Config{Role: Inviter, NewID: func() (ID, error) {
			if failing {
				return ID{}, errors.New("no entropy")
			}
			next := values[0]
			values = values[1:]
			return next, nil
		}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := machine.Start(epoch, Audio, 0, allowAll); err != nil {
			t.Fatal("first call was refused")
		}
		_, _ = machine.Hangup(epoch, machine.Snapshot().Call, EndHungUp)
		if _, err := machine.Start(epoch, Audio, 0, allowAll); !errors.Is(err, ErrUnavailable) {
			t.Fatal("a repeated call ID was used for a new call")
		}
		if _, err := machine.Start(epoch, Audio, 0, allowAll); !errors.Is(err, ErrUnavailable) {
			t.Fatal("a zero call ID was used for a new call")
		}
		// Ringing, with the next answer values being a used ID and then zero.
		machine.Handle(epoch.Add(time.Minute), Frame{Kind: KindOffer, Call: testID(9), Media: Audio}, allowAll)
		values = []ID{testID(1), {}}
		for _, why := range []string{"an already used value", "a zero value"} {
			if effects, err := machine.Accept(epoch.Add(time.Minute), testID(9), Audio, 0, allowAll); !errors.Is(err, ErrUnavailable) || effects.OpenMedia != nil {
				t.Fatalf("a call was answered with %s", why)
			}
		}
		failing = true
		if effects, err := machine.Accept(epoch.Add(time.Minute), testID(9), Audio, 0, allowAll); !errors.Is(err, ErrUnavailable) || effects.OpenMedia != nil {
			t.Fatal("a call was answered without a random source")
		}
		if machine.Snapshot().State != Incoming {
			t.Fatal("a failed answer disturbed the ringing call")
		}
	})
}

// A setting switched off while a call rings must win over an answer that is
// already on its way: the late answer starts no media.
func TestALateAnswerRechecksLocalSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		media   Media
		codecs  Codec
		after   Env
		expects End
	}{
		"calls switched off":             {Audio, 0, callsOff, EndInterrupted},
		"video switched off, video call": {Video, VP8, audioOnly, EndInterrupted},
	} {
		caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
		id := ring(t, caller, callee, tc.media, tc.codecs)
		accepted, err := callee.machine.Accept(epoch, id, tc.media, tc.codecs, callee.env)
		if err != nil {
			t.Fatal(err)
		}
		callee.note(accepted)
		caller.env = tc.after // switched off before the answer arrives
		back := deliver(t, epoch, accepted, caller)
		if back.OpenMedia != nil || caller.opened != 0 {
			t.Fatalf("%s: a late answer opened media", name)
		}
		if caller.machine.Snapshot().State != Idle || caller.machine.Snapshot().Last.End != tc.expects {
			t.Fatalf("%s: caller state %d, end %d", name, caller.machine.Snapshot().State, caller.machine.Snapshot().Last.End)
		}
		if len(back.Send) != 1 || back.Send[0].Kind != KindHangup {
			t.Fatalf("%s: the answering side was not told to hang up", name)
		}
		if got := deliver(t, epoch, back, callee); !got.CloseMedia || callee.machine.Snapshot().State != Idle {
			t.Fatalf("%s: the answering side kept its media open", name)
		}
	}
	// Video switched off does not affect a voice answer to a video offer.
	caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	id := ring(t, caller, callee, Video, VP8)
	accepted, _ := callee.machine.Accept(epoch, id, Audio, 0, callee.env)
	callee.note(accepted)
	caller.env = audioOnly
	if back := deliver(t, epoch, accepted, caller); back.OpenMedia == nil || back.OpenMedia.Video {
		t.Fatal("a voice answer was refused because video was switched off")
	}
}

// The caller cancels at the moment the callee answers. The callee must not sit
// in "connecting" with a media flow open until the connect timeout.
func TestACancelCrossingAnAnswerEndsTheAnsweredCallAtOnce(t *testing.T) {
	caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	id := ring(t, caller, callee, Audio, 0)
	cancelled, err := caller.machine.Hangup(epoch, id, EndHungUp)
	if err != nil || cancelled.Send[0].Kind != KindCancel {
		t.Fatal("cancel was not produced")
	}
	caller.note(cancelled)
	accepted, err := callee.machine.Accept(epoch, id, Audio, 0, callee.env) // before the cancel arrives
	if err != nil || accepted.OpenMedia == nil {
		t.Fatal("answer was refused")
	}
	callee.note(accepted)
	got := deliver(t, epoch, cancelled, callee)
	if !got.CloseMedia || callee.machine.Snapshot().State != Idle || callee.machine.Snapshot().Last.End != EndMissed || !callee.machine.Deadline().IsZero() {
		t.Fatalf("a crossed cancel left the answering side in state %d with media open=%v", callee.machine.Snapshot().State, !got.CloseMedia)
	}
	// The answer reaches a caller that has already gone idle: nothing happens.
	if back := deliver(t, epoch, accepted, caller); back.OpenMedia != nil || len(back.Send) != 0 || caller.machine.Snapshot().State != Idle {
		t.Fatal("an answer to a cancelled call started something on the caller")
	}
	if caller.opened != 0 || callee.opened != callee.closed {
		t.Fatal("media accounting is wrong after a crossed cancel")
	}
	// A cancel can never end a call from the side that placed it.
	caller, callee = newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	id = ring(t, caller, callee, Audio, 0)
	answer(t, caller, callee, id, Audio, 0)
	if got := caller.note(caller.machine.Handle(epoch, Frame{Kind: KindCancel, Call: id}, caller.env)); got.Changed || caller.machine.Snapshot().State != Connecting {
		t.Fatal("the callee cancelled the caller's call")
	}
}

func TestSwitchingSettingsOffEndsTheCallInEveryState(t *testing.T) {
	type stage struct {
		name  string
		build func() (subject *side, peer *side, id ID)
		frame Kind
		end   End
	}
	stages := []stage{
		{"outgoing", func() (*side, *side, ID) {
			caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
			return caller, callee, ring(t, caller, callee, Audio, 0)
		}, KindCancel, EndInterrupted},
		{"incoming", func() (*side, *side, ID) {
			caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
			return callee, caller, ring(t, caller, callee, Audio, 0)
		}, KindDecline, EndInterrupted},
		{"connecting", func() (*side, *side, ID) {
			caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
			id := ring(t, caller, callee, Audio, 0)
			answer(t, caller, callee, id, Audio, 0)
			return callee, caller, id
		}, KindHangup, EndInterrupted},
		{"active", func() (*side, *side, ID) {
			caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
			id := ring(t, caller, callee, Audio, 0)
			answer(t, caller, callee, id, Audio, 0)
			_, _ = caller.machine.MediaReady(epoch, id)
			_, _ = callee.machine.MediaReady(epoch, id)
			return caller, callee, id
		}, KindHangup, EndInterrupted},
	}
	for _, s := range stages {
		subject, peer, id := s.build()
		if got := subject.machine.Enforce(epoch, allowAll); got.Changed || len(got.Send) != 0 {
			t.Fatalf("%s: enforcing unchanged settings did something", s.name)
		}
		if got := subject.machine.Enforce(epoch, audioOnly); got.Changed || len(got.Send) != 0 {
			t.Fatalf("%s: switching video off disturbed a voice call", s.name)
		}
		wasOpen := subject.opened-subject.closed == 1
		effects := subject.note(subject.machine.Enforce(epoch, callsOff))
		if len(effects.Send) != 1 || effects.Send[0].Kind != s.frame || effects.Send[0].Call != id {
			t.Fatalf("%s: calls-off sent %+v", s.name, effects.Send)
		}
		if effects.CloseMedia != wasOpen || subject.machine.Snapshot().State != Idle || subject.machine.Snapshot().Last.End != s.end {
			t.Fatalf("%s: calls-off left state %d, media closed=%v", s.name, subject.machine.Snapshot().State, effects.CloseMedia)
		}
		deliver(t, epoch, effects, peer)
		if peer.machine.Snapshot().State != Idle || peer.opened != peer.closed {
			t.Fatalf("%s: the peer was left in a call", s.name)
		}
	}
	if got := newSide(t, Inviter, allowAll).machine.Enforce(epoch, callsOff); got.Changed || len(got.Send) != 0 {
		t.Fatal("enforcing settings on an idle slot did something")
	}

	// Video switched off: a ringing video call is offered as voice; any other video call ends.
	caller, callee := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	id := ring(t, caller, callee, Video, VP8)
	if got := callee.machine.Enforce(epoch, audioOnly); !got.Changed || len(got.Send) != 0 || callee.machine.Snapshot().Media != Audio || callee.machine.Snapshot().State != Incoming {
		t.Fatal("a ringing video call was not downgraded to voice")
	}
	if _, err := callee.machine.Accept(epoch, id, Video, VP8, audioOnly); !errors.Is(err, ErrNotAllowed) {
		t.Fatal("the downgraded call could be answered as video")
	}
	if got := caller.note(caller.machine.Enforce(epoch, audioOnly)); len(got.Send) != 1 || got.Send[0].Kind != KindCancel || caller.machine.Snapshot().State != Idle {
		t.Fatal("an outgoing video call survived video being switched off")
	}
	caller, callee = newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	id = ring(t, caller, callee, Video, VP8)
	answer(t, caller, callee, id, Video, VP8)
	if got := callee.note(callee.machine.Enforce(epoch, audioOnly)); !got.CloseMedia || len(got.Send) != 1 || got.Send[0].Kind != KindHangup {
		t.Fatal("an answered video call survived video being switched off")
	}
}

// Both people call at once, but the joiner has since switched calls off.
func TestSimultaneousCallWithCallsSwitchedOffAnswersNothing(t *testing.T) {
	inviter, joiner := newSide(t, Inviter, allowAll), newSide(t, Joiner, allowAll)
	fromInviter := mustStart(t, inviter, epoch, Audio, 0)
	fromJoiner := mustStart(t, joiner, epoch, Audio, 0)
	joinerID := joiner.machine.Snapshot().Call
	joiner.env = callsOff
	reply := deliver(t, epoch, fromInviter, joiner)
	if reply.OpenMedia != nil || joiner.opened != 0 || joiner.machine.Snapshot().State != Idle || joiner.machine.Snapshot().Last.End != EndInterrupted {
		t.Fatal("a device with calls switched off answered a simultaneous call")
	}
	if len(reply.Send) != 2 || reply.Send[0].Kind != KindCancel || reply.Send[0].Call != joinerID || reply.Send[1].Kind != KindDecline || reply.Send[1].Reason != ReasonUnavailable {
		t.Fatalf("expected its own call cancelled and the other refused, got %+v", reply.Send)
	}
	deliver(t, epoch, fromJoiner, inviter)
	deliver(t, epoch, reply, inviter)
	if inviter.machine.Snapshot().State != Idle || inviter.machine.Snapshot().Last.End != EndPeerUnavailable || inviter.opened != 0 {
		t.Fatal("the other side was not told the contact is unavailable")
	}
}
