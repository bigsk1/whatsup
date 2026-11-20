// Package call holds one-to-one call signaling: bounded canonical frames and a
// deterministic state machine. It performs no I/O, opens no device, holds no
// key and knows nothing about transport. A frame or a state transition grants
// no permission by itself; the caller supplies local policy on every decision.
package call

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
)

// ErrFrame reports a call frame that is not exactly one canonical encoding.
var ErrFrame = errors.New("invalid call signaling frame")

// ID identifies one call. It is random and never zero. The caller chooses it.
// It is not by itself enough to key media: the answering side adds its own
// random Answer value, so each side contributes fresh randomness to every call
// and neither can make the other derive the same media keys twice.
type ID [16]byte

func (id ID) IsZero() bool { return id == ID{} }

// String is the canonical unpadded URL-safe form used at the desktop boundary.
func (id ID) String() string { return base64.RawURLEncoding.EncodeToString(id[:]) }

// ParseID accepts only the canonical 22-character form of a nonzero ID.
func ParseID(text string) (ID, bool) {
	var id ID
	if len(text) != 22 {
		return id, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(text)
	if err != nil || len(raw) != len(id) || base64.RawURLEncoding.EncodeToString(raw) != text {
		return id, false
	}
	copy(id[:], raw)
	return id, !id.IsZero()
}

// Media is what a call carries. Video always includes audio.
type Media uint8

const (
	Audio Media = 1
	Video Media = 2
)

func (m Media) valid() bool { return m == Audio || m == Video }

// Codec is a bit set of video codecs. Audio is always Opus and is not negotiated.
type Codec uint8

const (
	VP8  Codec = 1
	VP9  Codec = 2
	H264 Codec = 4

	knownCodecs = VP8 | VP9 | H264
)

func (c Codec) single() bool { return c == VP8 || c == VP9 || c == H264 }

// Kind is a signaling frame type. Unknown kinds are rejected before any body is read.
type Kind uint8

const (
	KindOffer Kind = iota + 1
	KindRinging
	KindAccept
	KindDecline
	KindCancel
	KindHangup
	KindMediaState
	KindKeyframeRequest
	KindReceiverReport
)

// Reason is the wire reason carried by Decline and Hangup.
type Reason uint8

const (
	ReasonDeclined    Reason = 1 // the person declined
	ReasonBusy        Reason = 2 // already in a call, or offers arrived too quickly
	ReasonUnavailable Reason = 3 // calls are switched off on that device
	ReasonEnded       Reason = 4 // ordinary hang-up
	ReasonDeviceError Reason = 5 // microphone or camera failed
	ReasonMediaFailed Reason = 6 // media never started or stalled
	ReasonProtocol    Reason = 7 // the other side sent something invalid
)

func declineReason(r Reason) bool {
	return r == ReasonDeclined || r == ReasonBusy || r == ReasonUnavailable
}

func hangupReason(r Reason) bool {
	return r == ReasonEnded || r == ReasonDeviceError || r == ReasonMediaFailed || r == ReasonProtocol
}

// Frame is one decoded signaling message. Only the fields its Kind defines are
// meaningful; Validate rejects any other field being set.
type Frame struct {
	Kind     Kind
	Call     ID
	Media    Media  // Offer, Accept
	Codecs   Codec  // Offer: every video codec offered. Accept: the one chosen.
	Answer   ID     // Accept: the answering side's fresh random value, never zero
	Reason   Reason // Decline, Hangup
	MicMuted bool   // MediaState
	CameraOn bool   // MediaState
	Loss     uint8  // ReceiverReport: percent, 0-100
	JitterMS uint16 // ReceiverReport
}

const (
	headerSize     = 1 + 16 + 1
	acceptBodySize = 2 + 16
	// MaxFrameSize bounds every encoded frame.
	MaxFrameSize = headerSize + acceptBodySize

	flagMicMuted = 1
	flagCameraOn = 2
)

func bodySize(kind Kind) (int, bool) {
	switch kind {
	case KindOffer:
		return 2, true
	case KindAccept:
		return acceptBodySize, true
	case KindDecline, KindHangup, KindMediaState:
		return 1, true
	case KindReceiverReport:
		return 3, true
	case KindRinging, KindCancel, KindKeyframeRequest:
		return 0, true
	}
	return 0, false
}

// Validate reports whether the frame is one this version would send.
func (f Frame) Validate() error {
	if _, known := bodySize(f.Kind); !known || f.Call.IsZero() {
		return ErrFrame
	}
	expect := Frame{Kind: f.Kind, Call: f.Call}
	switch f.Kind {
	case KindOffer:
		if !f.Media.valid() || f.Codecs&^knownCodecs != 0 || (f.Media == Video) != (f.Codecs != 0) {
			return ErrFrame
		}
		expect.Media, expect.Codecs = f.Media, f.Codecs
	case KindAccept:
		if !f.Media.valid() || (f.Media == Video && !f.Codecs.single()) || (f.Media == Audio && f.Codecs != 0) || f.Answer.IsZero() {
			return ErrFrame
		}
		expect.Media, expect.Codecs, expect.Answer = f.Media, f.Codecs, f.Answer
	case KindDecline:
		if !declineReason(f.Reason) {
			return ErrFrame
		}
		expect.Reason = f.Reason
	case KindHangup:
		if !hangupReason(f.Reason) {
			return ErrFrame
		}
		expect.Reason = f.Reason
	case KindMediaState:
		expect.MicMuted, expect.CameraOn = f.MicMuted, f.CameraOn
	case KindReceiverReport:
		if f.Loss > 100 {
			return ErrFrame
		}
		expect.Loss, expect.JitterMS = f.Loss, f.JitterMS
	}
	if f != expect {
		return ErrFrame
	}
	return nil
}

// Encode returns the single canonical encoding of a valid frame.
func Encode(f Frame) ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	size, _ := bodySize(f.Kind)
	wire := make([]byte, 0, headerSize+size)
	wire = append(wire, byte(f.Kind))
	wire = append(wire, f.Call[:]...)
	wire = append(wire, byte(size))
	switch f.Kind {
	case KindOffer:
		wire = append(wire, byte(f.Media), byte(f.Codecs))
	case KindAccept:
		wire = append(wire, byte(f.Media), byte(f.Codecs))
		wire = append(wire, f.Answer[:]...)
	case KindDecline, KindHangup:
		wire = append(wire, byte(f.Reason))
	case KindMediaState:
		var flags byte
		if f.MicMuted {
			flags |= flagMicMuted
		}
		if f.CameraOn {
			flags |= flagCameraOn
		}
		wire = append(wire, flags)
	case KindReceiverReport:
		wire = append(wire, f.Loss)
		wire = binary.BigEndian.AppendUint16(wire, f.JitterMS)
	}
	return wire, nil
}

// Read decodes exactly one frame. The kind and the declared length are checked
// against each other before the body is read, so a hostile length cannot make
// the reader wait for, or allocate, more than the fixed maximum.
func Read(reader io.Reader) (Frame, error) {
	var header [headerSize]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return Frame{}, ErrFrame
	}
	f := Frame{Kind: Kind(header[0])}
	copy(f.Call[:], header[1:17])
	size, known := bodySize(f.Kind)
	if !known || int(header[17]) != size || f.Call.IsZero() {
		return Frame{}, ErrFrame
	}
	var body [acceptBodySize]byte
	if _, err := io.ReadFull(reader, body[:size]); err != nil {
		return Frame{}, ErrFrame
	}
	switch f.Kind {
	case KindOffer:
		f.Media, f.Codecs = Media(body[0]), Codec(body[1])
	case KindAccept:
		f.Media, f.Codecs = Media(body[0]), Codec(body[1])
		copy(f.Answer[:], body[2:acceptBodySize])
	case KindDecline, KindHangup:
		f.Reason = Reason(body[0])
	case KindMediaState:
		if body[0]&^(flagMicMuted|flagCameraOn) != 0 {
			return Frame{}, ErrFrame
		}
		f.MicMuted, f.CameraOn = body[0]&flagMicMuted != 0, body[0]&flagCameraOn != 0
	case KindReceiverReport:
		f.Loss, f.JitterMS = body[0], binary.BigEndian.Uint16(body[1:3])
	}
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}
