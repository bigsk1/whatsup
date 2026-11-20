package callmedia

import (
	"errors"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

const (
	// MaxDatagram is the largest tunnel UDP payload (Tailcat's ceiling). A
	// transport test asserts the two values stay equal.
	MaxDatagram = 1232
	// datagramBudget leaves headroom below the ceiling for every packet we build.
	datagramBudget = 1200
	rtpHeaderSize  = 12
	tagSize        = 16
	minDatagram    = rtpHeaderSize + tagSize + 1

	// MaxAudioFrame bounds one encoded audio frame. Opus at call bitrates is
	// about 80 bytes per 20 ms.
	MaxAudioFrame = 500
	// MaxVideoFrame bounds one encoded video frame, keyframes included.
	MaxVideoFrame = 256 << 10
	// AudioFrameSamples is the fixed audio frame length in 48 kHz samples (20 ms).
	AudioFrameSamples = 960

	videoFragment = datagramBudget - rtpHeaderSize - tagSize - 1
	maxRedundant  = 255

	// Fixed stream identifiers. Under AES-GCM SRTP each (key, SSRC, packet
	// index) must be used once; directions have different keys and each stream
	// below has its own sequence space.
	ssrcAudio     = 1
	ssrcVideo     = 2
	ssrcKeepalive = 3
	typeAudio     = 111
	typeVideo     = 96
	typeKeepalive = 100

	flagStart = 1
	flagKey   = 2

	keyframeRequestGap = 500 * time.Millisecond
)

var (
	// ErrFrame reports media the sender refuses to packetize.
	ErrFrame = errors.New("call media frame is empty or too large")
	// ErrDatagram reports a received datagram that was dropped: malformed,
	// not authentic, replayed or outside the fixed profile.
	ErrDatagram = errors.New("call media datagram rejected")
)

// Sender seals one direction of one call. It is not safe for concurrent use
// and must live exactly as long as the call: sequence numbers never restart
// under a key, so a Sender is never rebuilt from the same Keys.
type Sender struct {
	context                      *srtp.Context
	audioSeq, videoSeq, aliveSeq uint16
	previous                     []byte
}

// NewSender builds the sending side from this device's send key.
func NewSender(keys *Keys) (*Sender, error) {
	if keys == nil || !keys.ready {
		return nil, ErrKeys
	}
	context, err := srtp.CreateContext(keys.send.key[:], keys.send.salt[:], profile)
	if err != nil {
		return nil, ErrKeys
	}
	return &Sender{context: context}, nil
}

func (s *Sender) seal(ssrc uint32, payloadType uint8, sequence *uint16, timestamp uint32, marker bool, payload []byte) ([]byte, error) {
	*sequence++
	plain, err := (&rtp.Packet{
		Header:  rtp.Header{Version: 2, Marker: marker, PayloadType: payloadType, SequenceNumber: *sequence, Timestamp: timestamp, SSRC: ssrc},
		Payload: payload,
	}).Marshal()
	if err != nil {
		return nil, ErrFrame
	}
	sealed, err := s.context.EncryptRTP(nil, plain, nil)
	if err != nil || len(sealed) > MaxDatagram {
		return nil, ErrFrame
	}
	return sealed, nil
}

// Audio seals one encoded audio frame as one datagram. The previous frame
// rides along so the receiver can fill a single lost packet.
func (s *Sender) Audio(frame []byte, timestamp uint32) ([]byte, error) {
	if s == nil || len(frame) == 0 || len(frame) > MaxAudioFrame {
		return nil, ErrFrame
	}
	redundant := s.previous
	if len(redundant) > maxRedundant {
		redundant = nil
	}
	payload := make([]byte, 0, 1+len(redundant)+len(frame))
	payload = append(payload, byte(len(redundant)))
	payload = append(payload, redundant...)
	payload = append(payload, frame...)
	sealed, err := s.seal(ssrcAudio, typeAudio, &s.audioSeq, timestamp, false, payload)
	if err != nil {
		return nil, err
	}
	s.previous = append(s.previous[:0], frame...)
	return sealed, nil
}

// Video seals one encoded video frame as one or more datagrams, in send order.
func (s *Sender) Video(frame []byte, timestamp uint32, key bool) ([][]byte, error) {
	if s == nil || len(frame) == 0 || len(frame) > MaxVideoFrame {
		return nil, ErrFrame
	}
	datagrams := make([][]byte, 0, len(frame)/videoFragment+1)
	for offset := 0; offset < len(frame); offset += videoFragment {
		end := min(offset+videoFragment, len(frame))
		var flags byte
		if offset == 0 {
			flags |= flagStart
		}
		if key {
			flags |= flagKey
		}
		payload := make([]byte, 0, 1+end-offset)
		payload = append(payload, flags)
		payload = append(payload, frame[offset:end]...)
		sealed, err := s.seal(ssrcVideo, typeVideo, &s.videoSeq, timestamp, end == len(frame), payload)
		if err != nil {
			return nil, err
		}
		datagrams = append(datagrams, sealed)
	}
	return datagrams, nil
}

// Keepalive seals an empty liveness datagram for stretches with no media,
// such as a muted microphone on a voice call.
func (s *Sender) Keepalive() ([]byte, error) {
	if s == nil {
		return nil, ErrFrame
	}
	return s.seal(ssrcKeepalive, typeKeepalive, &s.aliveSeq, 0, false, []byte{0})
}

// Kind says what a received frame is.
type Kind uint8

const (
	FrameAudio Kind = iota + 1
	FrameVideo
)

// Frame is one complete encoded frame ready for the decoder.
type Frame struct {
	Kind      Kind
	Timestamp uint32
	Key       bool // video: decodable without earlier frames
	Recovered bool // audio: rebuilt from the following packet's redundant copy
	Data      []byte
}

// Received is the outcome of one authentic datagram.
type Received struct {
	Frames []Frame
	// NeedKeyframe asks the caller to request a fresh video keyframe from the
	// peer, because video was lost and later frames cannot be decoded.
	NeedKeyframe bool
}

// Stats is reception quality since the previous Stats call.
type Stats struct {
	Loss     uint8  // percent of expected audio and video packets that never arrived
	JitterMS uint16 // smoothed audio interarrival jitter
	Packets  uint64 // authentic datagrams accepted in the interval
}

type sequenceTrack struct {
	started  bool
	first    uint64
	highest  uint64
	seen     uint64 // bit i set: packet highest-i arrived or was recovered
	received uint64
	baseline uint64 // expected count at the previous Stats call
	counted  uint64 // received count at the previous Stats call
}

// index extends a 16-bit RTP sequence number to the packet's full index: the
// candidate nearest the highest index seen on this stream (RFC 3711 section
// 3.3.1). The sequence number wraps about every 22 minutes of audio, and a
// packet from just before a wrap can arrive just after it. Taking the cycle
// from the stream's current position instead would place that late packet a
// whole cycle in the future and make every later packet look stale.
func (t *sequenceTrack) index(sequence uint16) uint64 {
	if !t.started {
		return uint64(sequence)
	}
	cycle := t.highest >> 16
	distance := func(candidate uint64) uint64 {
		if candidate > t.highest {
			return candidate - t.highest
		}
		return t.highest - candidate
	}
	best := cycle<<16 | uint64(sequence)
	if cycle > 0 {
		if earlier := (cycle-1)<<16 | uint64(sequence); distance(earlier) < distance(best) {
			best = earlier
		}
	}
	if later := (cycle+1)<<16 | uint64(sequence); distance(later) < distance(best) {
		best = later
	}
	return best
}

// accept records a packet index and reports whether it is new.
func (t *sequenceTrack) accept(index uint64) bool {
	if !t.started {
		t.started, t.first, t.highest, t.seen = true, index, index, 1
		t.received++
		return true
	}
	if index > t.highest {
		shift := index - t.highest
		if shift >= 64 {
			t.seen = 0
		} else {
			t.seen <<= shift
		}
		t.seen |= 1
		t.highest = index
		t.received++
		return true
	}
	behind := t.highest - index
	if behind >= 64 || t.seen&(1<<behind) != 0 {
		return false
	}
	t.seen |= 1 << behind
	t.received++
	return true
}

func (t *sequenceTrack) has(index uint64) bool {
	if !t.started || index > t.highest {
		return false
	}
	behind := t.highest - index
	// Anything older than the window is treated as already handled.
	return behind >= 64 || t.seen&(1<<behind) != 0
}

func (t *sequenceTrack) interval() (expected, received uint64) {
	if !t.started {
		return 0, 0
	}
	total := t.highest - t.first + 1
	expected, received = total-t.baseline, t.received-t.counted
	t.baseline, t.counted = total, t.received
	return expected, received
}

// Receiver opens one direction of one call. It is not safe for concurrent use.
type Receiver struct {
	context *srtp.Context
	audio   sequenceTrack
	video   sequenceTrack

	// Video reassembly accepts fragments in order only. A gap drops the
	// partial frame and holds back delta frames until the next keyframe.
	assembling   bool
	frameKey     bool
	frameTime    uint32
	nextFragment uint64
	lastFragment uint64
	haveVideo    bool
	waitingKey   bool
	buffer       []byte
	lastRequest  time.Time

	jitter      float64
	lastArrival time.Time
	lastAudioTS uint32
	haveAudio   bool
	packets     uint64
}

// NewReceiver builds the receiving side from this device's receive key, with
// replay protection always on.
func NewReceiver(keys *Keys) (*Receiver, error) {
	if keys == nil || !keys.ready {
		return nil, ErrKeys
	}
	context, err := srtp.CreateContext(keys.receive.key[:], keys.receive.salt[:], profile, srtp.SRTPReplayProtection(replayWindow))
	if err != nil {
		return nil, ErrKeys
	}
	return &Receiver{context: context, waitingKey: true}, nil
}

// Open authenticates one datagram and returns any frames it completes. Every
// rejection returns ErrDatagram and changes no reassembly state.
func (r *Receiver) Open(datagram []byte, now time.Time) (Received, error) {
	var out Received
	if r == nil || len(datagram) < minDatagram || len(datagram) > MaxDatagram {
		return out, ErrDatagram
	}
	// Cheap fixed-profile checks before any cryptography. These fields are
	// not yet authenticated; they only decide whether to try.
	var header rtp.Header
	size, err := header.Unmarshal(datagram)
	if err != nil || size != rtpHeaderSize || header.Version != 2 || header.Padding || header.Extension || len(header.CSRC) != 0 {
		return out, ErrDatagram
	}
	switch {
	case header.SSRC == ssrcAudio && header.PayloadType == typeAudio && !header.Marker:
	case header.SSRC == ssrcVideo && header.PayloadType == typeVideo:
	case header.SSRC == ssrcKeepalive && header.PayloadType == typeKeepalive && !header.Marker:
	default:
		return out, ErrDatagram
	}
	var authenticated rtp.Header
	plain, err := r.context.DecryptRTP(nil, datagram, &authenticated)
	if err != nil || len(plain) <= rtpHeaderSize || authenticated.SSRC != header.SSRC || authenticated.SequenceNumber != header.SequenceNumber {
		return out, ErrDatagram
	}
	payload := plain[rtpHeaderSize:]
	r.packets++
	// The packet is authentic, so its sequence number is the sender's. Each
	// stream extends it against its own history; the SRTP context's current
	// rollover counter is not the counter this particular packet was sent under.
	switch header.SSRC {
	case ssrcAudio:
		r.openAudio(r.audio.index(header.SequenceNumber), header.Timestamp, payload, now, &out)
	case ssrcVideo:
		r.openVideo(r.video.index(header.SequenceNumber), header.Timestamp, header.Marker, payload, now, &out)
	}
	return out, nil
}

func (r *Receiver) openAudio(index uint64, timestamp uint32, payload []byte, now time.Time, out *Received) {
	redundant := int(payload[0])
	if len(payload) < 1+redundant+1 || len(payload)-1-redundant > MaxAudioFrame {
		return
	}
	newest := !r.audio.started || index > r.audio.highest
	if !r.audio.accept(index) {
		return // already delivered, as itself or as a recovered copy
	}
	if newest {
		if r.haveAudio {
			// RFC 3550 interarrival jitter, in milliseconds at 48 kHz.
			transit := now.Sub(r.lastArrival).Seconds()*1000 - float64(int32(timestamp-r.lastAudioTS))/48
			if transit < 0 {
				transit = -transit
			}
			r.jitter += (transit - r.jitter) / 16
		}
		r.lastArrival, r.lastAudioTS, r.haveAudio = now, timestamp, true
		if redundant > 0 && index > 0 && index-1 >= r.audio.first && !r.audio.has(index-1) {
			r.audio.accept(index - 1)
			// The recovered copy is not a received packet for loss accounting.
			r.audio.received--
			out.Frames = append(out.Frames, Frame{Kind: FrameAudio, Timestamp: timestamp - AudioFrameSamples, Recovered: true,
				Data: append([]byte(nil), payload[1:1+redundant]...)})
		}
	}
	out.Frames = append(out.Frames, Frame{Kind: FrameAudio, Timestamp: timestamp, Data: append([]byte(nil), payload[1+redundant:]...)})
}

func (r *Receiver) videoLost(now time.Time, out *Received) {
	r.assembling, r.buffer = false, r.buffer[:0]
	r.waitingKey = true
	if r.lastRequest.IsZero() || now.Sub(r.lastRequest) >= keyframeRequestGap {
		r.lastRequest = now
		out.NeedKeyframe = true
	}
}

func (r *Receiver) openVideo(index uint64, timestamp uint32, marker bool, payload []byte, now time.Time, out *Received) {
	flags, fragment := payload[0], payload[1:]
	if flags&^(flagStart|flagKey) != 0 || len(fragment) == 0 || len(fragment) > videoFragment {
		return
	}
	if !r.video.accept(index) {
		return
	}
	start := flags&flagStart != 0
	switch {
	case start:
		interrupted := r.assembling
		skipped := r.haveVideo && index != r.lastFragment+1
		r.assembling, r.frameKey, r.frameTime = true, flags&flagKey != 0, timestamp
		r.buffer = append(r.buffer[:0], fragment...)
		if (interrupted || skipped) && !r.frameKey {
			// Something before this delta frame is missing.
			r.videoLost(now, out)
			r.lastFragment, r.haveVideo = index, true
			return
		}
	case !r.assembling || index != r.nextFragment || timestamp != r.frameTime || (flags&flagKey != 0) != r.frameKey:
		wasWaiting := r.waitingKey && !r.assembling
		r.lastFragment, r.haveVideo = index, true
		if !wasWaiting {
			r.videoLost(now, out)
		}
		return
	default:
		if len(r.buffer)+len(fragment) > MaxVideoFrame {
			r.videoLost(now, out)
			r.lastFragment, r.haveVideo = index, true
			return
		}
		r.buffer = append(r.buffer, fragment...)
	}
	r.nextFragment, r.lastFragment, r.haveVideo = index+1, index, true
	if !marker {
		return
	}
	r.assembling = false
	if r.frameKey {
		r.waitingKey = false
	}
	if r.waitingKey {
		// A delta frame that depends on something we never showed.
		if r.lastRequest.IsZero() || now.Sub(r.lastRequest) >= keyframeRequestGap {
			r.lastRequest = now
			out.NeedKeyframe = true
		}
		return
	}
	out.Frames = append(out.Frames, Frame{Kind: FrameVideo, Timestamp: r.frameTime, Key: r.frameKey, Data: append([]byte(nil), r.buffer...)})
}

// Stats reports reception quality since the previous call and starts a new interval.
func (r *Receiver) Stats() Stats {
	audioExpected, audioReceived := r.audio.interval()
	videoExpected, videoReceived := r.video.interval()
	expected, received := audioExpected+videoExpected, audioReceived+videoReceived
	stats := Stats{Packets: r.packets}
	r.packets = 0
	if expected > 0 && received < expected {
		stats.Loss = uint8((expected - received) * 100 / expected)
	}
	jitter := r.jitter
	if jitter > 65535 {
		jitter = 65535
	}
	stats.JitterMS = uint16(jitter)
	return stats
}
