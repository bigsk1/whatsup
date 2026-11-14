package transfer

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestFrameRoundTripAndStrictBoundaries(t *testing.T) {
	o := fixtureOffer(11, bytes.Repeat([]byte{42}, ChunkBytes))
	frames := []Frame{
		{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o},
		control(AcceptFrame, o, 0, 0), control(CheckpointFrame, o, 1, 0),
		{Kind: ChunkFrame, ID: o.ID, SHA256: o.SHA256, Index: 0, Data: bytes.Repeat([]byte{42}, ChunkBytes)},
		control(FinishFrame, o, 0, 0), control(CompleteFrame, o, 0, 0),
		control(PauseFrame, o, 0, ReasonPaused), control(DeclineFrame, o, 0, ReasonDeclined), control(FailureFrame, o, 0, ReasonCapacity),
		control(WithdrawFrame, o, 0, 0), control(WithdrawAckFrame, o, 0, ReasonDeclined), control(WithdrawAckFrame, o, 0, ReasonCompleted),
	}
	for _, f := range frames {
		if f.Kind == AcceptFrame || f.Kind == CheckpointFrame || f.Kind == ChunkFrame || f.Kind == FinishFrame {
			f.Credit = [16]byte{1}
		}
		var wire bytes.Buffer
		if err := WriteFrame(&wire, f); err != nil {
			t.Fatal(err)
		}
		encoded := bytes.Clone(wire.Bytes())
		decoded, err := ReadFrame(&wire)
		if err != nil || !reflect.DeepEqual(decoded, f) {
			t.Fatal("frame did not round trip")
		}
		if _, err = ReadFrame(bytes.NewReader(encoded[:len(encoded)-1])); err == nil {
			t.Fatal("truncated frame accepted")
		}
		body, _ := EncodeFrame(f)
		if _, err = DecodeFrame(append(body, 0)); err == nil {
			t.Fatal("trailing bytes accepted")
		}
		if err = WriteFrame(shortWriter{}, f); !errors.Is(err, io.ErrShortWrite) {
			t.Fatal("short write accepted")
		}
	}
	var oversized [4]byte
	binary.BigEndian.PutUint32(oversized[:], MaxFrameBytes+1)
	if _, err := ReadFrame(bytes.NewReader(oversized[:])); !errors.Is(err, ErrProtocol) {
		t.Fatal("oversized allocation accepted")
	}
	invalid := []Frame{
		control(DeclineFrame, o, 0, ReasonPaused), control(FailureFrame, o, 0, ReasonRelayApproval), control(PauseFrame, o, 0, ReasonIntegrity),
		{Kind: ChunkFrame, ID: o.ID, SHA256: o.SHA256, Data: make([]byte, ChunkBytes+1)},
		{Kind: ChunkFrame, ID: o.ID, SHA256: o.SHA256, Index: uint32(MaxFileBytes / ChunkBytes), Data: []byte{1}},
		{Kind: FinishFrame, ID: o.ID, SHA256: o.SHA256, Data: []byte{1}},
	}
	for _, f := range invalid {
		if f.Validate() == nil {
			t.Fatal("noncanonical frame accepted")
		}
	}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestOfferLimitsAndNoPeerControlledPaths(t *testing.T) {
	o := fixtureOffer(12, nil)
	o.Size = MaxFileBytes
	if o.Validate() != nil || o.Chunks() != 32768 {
		t.Fatal("2 GiB boundary rejected")
	}
	o.Size++
	if o.Validate() == nil {
		t.Fatal("file above 2 GiB accepted")
	}
	o.Size = 0
	for _, name := range []string{"../escape", `C:\secret`, "/tmp/private", "a\x00b", "a\u202eb", ".", "..", "trailing.", " leading", strings.Repeat("x", 256)} {
		o.Name = name
		if o.Validate() == nil {
			t.Fatal("unsafe attachment name accepted")
		}
	}
	o.Name = "synthetic.png"
	for _, media := range []string{"image/svg+xml; charset=utf-8", "text/html\nInjected:yes", "Image/PNG", "", strings.Repeat("x", 128)} {
		o.MediaType = media
		if o.Validate() == nil {
			t.Fatal("noncanonical media type accepted")
		}
	}
}

func FuzzDecodeFrame(f *testing.F) {
	o := fixtureOffer(13, []byte("synthetic"))
	encoded, _ := EncodeFrame(Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o})
	f.Add(encoded)
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, input []byte) {
		frame, err := DecodeFrame(input)
		if err != nil {
			return
		}
		again, err := EncodeFrame(frame)
		if err != nil || !bytes.Equal(input, again) {
			t.Fatal("accepted noncanonical attachment encoding")
		}
	})
}
