package call

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func testID(fill byte) ID {
	var id ID
	for i := range id {
		id[i] = fill
	}
	return id
}

func validFrames() []Frame {
	id := testID(7)
	return []Frame{
		{Kind: KindOffer, Call: id, Media: Audio},
		{Kind: KindOffer, Call: id, Media: Video, Codecs: VP8 | H264},
		{Kind: KindRinging, Call: id},
		{Kind: KindAccept, Call: id, Media: Audio, Answer: testID(8)},
		{Kind: KindAccept, Call: id, Media: Video, Codecs: VP9, Answer: testID(9)},
		{Kind: KindDecline, Call: id, Reason: ReasonDeclined},
		{Kind: KindDecline, Call: id, Reason: ReasonBusy},
		{Kind: KindDecline, Call: id, Reason: ReasonUnavailable},
		{Kind: KindCancel, Call: id},
		{Kind: KindHangup, Call: id, Reason: ReasonEnded},
		{Kind: KindHangup, Call: id, Reason: ReasonProtocol},
		{Kind: KindMediaState, Call: id},
		{Kind: KindMediaState, Call: id, MicMuted: true, CameraOn: true},
		{Kind: KindKeyframeRequest, Call: id},
		{Kind: KindReceiverReport, Call: id, Loss: 100, JitterMS: 65535},
	}
}

func TestFramesRoundTripCanonically(t *testing.T) {
	for _, frame := range validFrames() {
		wire, err := Encode(frame)
		if err != nil {
			t.Fatalf("valid frame %+v was refused", frame)
		}
		if len(wire) > MaxFrameSize {
			t.Fatalf("frame kind %d is %d bytes, over the fixed maximum", frame.Kind, len(wire))
		}
		reader := bytes.NewReader(append(wire, 0xEE))
		decoded, err := Read(reader)
		if err != nil || decoded != frame {
			t.Fatalf("frame kind %d did not round trip: %+v / %v", frame.Kind, decoded, err)
		}
		if reader.Len() != 1 {
			t.Fatalf("frame kind %d consumed bytes beyond its own encoding", frame.Kind)
		}
		again, _ := Encode(decoded)
		if !bytes.Equal(again, wire) {
			t.Fatalf("frame kind %d has more than one encoding", frame.Kind)
		}
	}
}

func TestInvalidFramesAreRefusedInBothDirections(t *testing.T) {
	id := testID(9)
	for name, frame := range map[string]Frame{
		"zero call ID":             {Kind: KindRinging},
		"unknown kind":             {Kind: 0, Call: id},
		"kind past the last":       {Kind: KindReceiverReport + 1, Call: id},
		"offer without media":      {Kind: KindOffer, Call: id},
		"offer unknown media":      {Kind: KindOffer, Call: id, Media: 3},
		"video offer no codec":     {Kind: KindOffer, Call: id, Media: Video},
		"audio offer with codec":   {Kind: KindOffer, Call: id, Media: Audio, Codecs: VP8},
		"offer unknown codec bit":  {Kind: KindOffer, Call: id, Media: Video, Codecs: 8},
		"accept two codecs":        {Kind: KindAccept, Call: id, Media: Video, Codecs: VP8 | VP9, Answer: testID(8)},
		"accept audio with codec":  {Kind: KindAccept, Call: id, Media: Audio, Codecs: VP8, Answer: testID(8)},
		"accept without answer":    {Kind: KindAccept, Call: id, Media: Audio},
		"offer with answer value":  {Kind: KindOffer, Call: id, Media: Audio, Answer: testID(8)},
		"hangup with answer value": {Kind: KindHangup, Call: id, Reason: ReasonEnded, Answer: testID(8)},
		"decline hangup reason":    {Kind: KindDecline, Call: id, Reason: ReasonEnded},
		"decline no reason":        {Kind: KindDecline, Call: id},
		"hangup decline reason":    {Kind: KindHangup, Call: id, Reason: ReasonBusy},
		"report loss over 100":     {Kind: KindReceiverReport, Call: id, Loss: 101},
		"ringing with stray field": {Kind: KindRinging, Call: id, Media: Audio},
		"cancel with stray reason": {Kind: KindCancel, Call: id, Reason: ReasonEnded},
		"offer with stray mute":    {Kind: KindOffer, Call: id, Media: Audio, MicMuted: true},
	} {
		if frame.Validate() == nil {
			t.Errorf("%s: frame validated", name)
		}
		if wire, err := Encode(frame); err == nil || wire != nil {
			t.Errorf("%s: frame encoded", name)
		}
	}

	good, _ := Encode(Frame{Kind: KindMediaState, Call: id, MicMuted: true})
	for name, mutate := range map[string]func([]byte) []byte{
		"truncated header":  func(w []byte) []byte { return w[:10] },
		"truncated body":    func(w []byte) []byte { return w[:len(w)-1] },
		"length too long":   func(w []byte) []byte { w[17] = 200; return w },
		"length too short":  func(w []byte) []byte { w[17] = 0; return w },
		"unknown kind":      func(w []byte) []byte { w[0] = 99; return w },
		"zero call ID":      func(w []byte) []byte { copy(w[1:17], make([]byte, 16)); return w },
		"unknown flag bits": func(w []byte) []byte { w[18] = 0x80; return w },
		"empty":             func([]byte) []byte { return nil },
	} {
		wire := mutate(bytes.Clone(good))
		if frame, err := Read(bytes.NewReader(wire)); !errors.Is(err, ErrFrame) || frame != (Frame{}) {
			t.Errorf("%s: malformed bytes decoded as %+v (%v)", name, frame, err)
		}
	}
}

// A declared length must never make the reader pull more than the fixed body.
func TestReadNeverConsumesBeyondTheFixedMaximum(t *testing.T) {
	wire := append([]byte{byte(KindRinging)}, bytes.Repeat([]byte{1}, 16)...)
	wire = append(wire, 255)
	reader := &countingReader{reader: io.MultiReader(bytes.NewReader(wire), neverEnding{})}
	if _, err := Read(reader); !errors.Is(err, ErrFrame) {
		t.Fatal("an oversized declared length was accepted")
	}
	if reader.read > MaxFrameSize {
		t.Fatalf("decoder read %d bytes for a refused frame", reader.read)
	}
}

type neverEnding struct{}

func (neverEnding) Read(p []byte) (int, error) { return len(p), nil }

type countingReader struct {
	reader io.Reader
	read   int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.reader.Read(p)
	c.read += n
	return n, err
}

func FuzzReadFrame(f *testing.F) {
	for _, frame := range validFrames() {
		wire, _ := Encode(frame)
		f.Add(wire)
	}
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xFF}, MaxFrameSize))
	f.Fuzz(func(t *testing.T, data []byte) {
		frame, err := Read(bytes.NewReader(data))
		if err != nil {
			if frame != (Frame{}) {
				t.Fatal("a refused frame leaked decoded fields")
			}
			return
		}
		wire, err := Encode(frame)
		if err != nil {
			t.Fatalf("decoded frame %+v cannot be encoded", frame)
		}
		if !bytes.HasPrefix(data, wire) {
			t.Fatal("decoded frame does not re-encode to the bytes it came from")
		}
	})
}
