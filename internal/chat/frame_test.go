package chat

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

const syntheticID = "AQEBAQEBAQEBAQEBAQEBAQ"

func TestFramingRoundTripAndBounds(t *testing.T) {
	frames := []Frame{
		{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: "Synthetic plain text\nwith a tab\tand 日本語"},
		{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: strings.Repeat("<", MaxTextBytes)},
		{Version: ProtocolVersion, Type: AckFrame, ID: syntheticID},
	}
	var stream bytes.Buffer
	for _, frame := range frames {
		if err := WriteFrame(&stream, frame); err != nil {
			t.Fatal(err)
		}
	}
	for _, expected := range frames {
		actual, err := ReadFrame(&stream)
		if err != nil || actual != expected {
			t.Fatalf("frame did not round trip: %v", err)
		}
	}
	if _, err := ReadFrame(&stream); !errors.Is(err, io.EOF) {
		t.Fatal("EOF not preserved")
	}
	for _, length := range []uint32{0, MaxFrameBytes + 1, ^uint32(0)} {
		var prefix [4]byte
		binary.BigEndian.PutUint32(prefix[:], length)
		reader := bytes.NewReader(append(prefix[:], []byte("body must remain unread")...))
		if _, err := ReadFrame(reader); !errors.Is(err, ErrFrame) || reader.Len() != len("body must remain unread") {
			t.Fatal("invalid length read or allocated a body")
		}
	}
	var truncated bytes.Buffer
	_ = binary.Write(&truncated, binary.BigEndian, uint32(10))
	truncated.WriteString("short")
	if _, err := ReadFrame(&truncated); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal("truncated frame accepted")
	}
	if err := WriteFrame(shortWriter{}, frames[0]); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal("short writer success accepted")
	}
}

type shortWriter struct{}

func (shortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestFrameParserRejectsAmbiguousOrActiveInputs(t *testing.T) {
	valid := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: "hello"}
	encoded, _ := EncodeFrame(valid)
	bad := []string{
		"", "null", "[]", string(encoded) + "{}", " " + string(encoded),
		strings.Replace(string(encoded), `"version":1`, `"version":2`, 1),
		strings.Replace(string(encoded), `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(string(encoded), `"id":`, `"ID":`, 1),
		strings.Replace(string(encoded), `"id":`, `"unknown":true,"id":`, 1),
		strings.Replace(string(encoded), `"message"`, `"execute"`, 1),
		strings.Replace(string(encoded), syntheticID, syntheticID+"=", 1),
		strings.Replace(string(encoded), `"hello"`, `""`, 1),
		strings.Replace(string(encoded), `"hello"`, `"\u0000"`, 1),
		strings.Replace(string(encoded), `"hello"`, `"\r"`, 1),
		string(bytes.Replace(encoded, []byte("hello"), []byte{255}, 1)),
		strings.Repeat("x", MaxFrameBytes+1),
	}
	for index, input := range bad {
		if _, err := DecodeFrame([]byte(input)); err == nil {
			t.Errorf("invalid frame %d accepted", index)
		}
	}
	for _, text := range []string{"", strings.Repeat("x", MaxTextBytes+1), "bad\x00text", "bad\x1btext", string([]byte{255})} {
		valid.Text = text
		if _, err := EncodeFrame(valid); err == nil {
			t.Fatal("invalid display text accepted")
		}
	}
	// HTML and Markdown-looking bytes remain inert text; this package has no
	// renderer, URL opener, preview engine, or remote content fetcher.
	valid.Text = `<script>alert(1)</script> ![remote](https://invalid.example/image.svg)`
	encoded, err := EncodeFrame(valid)
	decoded, parseErr := DecodeFrame(encoded)
	if err != nil || parseErr != nil || decoded.Text != valid.Text {
		t.Fatal("literal display text was executed or modified")
	}
	if _, err := EncodeFrame(Frame{Version: ProtocolVersion, Type: AckFrame, ID: syntheticID, Text: "unwanted"}); err == nil {
		t.Fatal("acknowledgement admitted text")
	}
}

func TestPeerDisplayControlsRejectedBeforeEncodingAndDecoding(t *testing.T) {
	for _, hidden := range []rune{
		0x0085, 0x009b, 0x00ad, 0x034f, 0x061c, 0x115f, 0x1160,
		0x17b4, 0x17b5, 0x180e, 0x200b, 0x200c, 0x200d, 0x200e, 0x200f,
		0x2028, 0x2029, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2060, 0x2066, 0x2067, 0x2068, 0x2069, 0x3164, 0xfeff, 0xffa0,
	} {
		text := "synthetic" + string(hidden) + "interface label"
		frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: text}
		if _, err := EncodeFrame(frame); !errors.Is(err, ErrText) {
			t.Errorf("display control U+%04X accepted for outgoing text", hidden)
		}
		// Bypass the encoder to exercise malicious peer input directly.
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeFrame(encoded); err == nil {
			t.Errorf("display control U+%04X accepted from peer", hidden)
		}
	}
	for _, text := range []string{"مرحبا بالعالم", "שלום עולם", "日本語", "cafe\u0301", "hello 👋", "line one\nline two\tvalue"} {
		frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: text}
		encoded, err := EncodeFrame(frame)
		if err != nil {
			t.Fatalf("ordinary display text rejected: %v", err)
		}
		if decoded, err := DecodeFrame(encoded); err != nil || decoded.Text != text {
			t.Fatal("ordinary display text did not round trip")
		}
	}
}

func FuzzDecodeFrame(f *testing.F) {
	f.Add([]byte(""))
	message, _ := EncodeFrame(Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: "synthetic"})
	ack, _ := EncodeFrame(Frame{Version: ProtocolVersion, Type: AckFrame, ID: syntheticID})
	f.Add(message)
	f.Add(ack)
	f.Fuzz(func(t *testing.T, encoded []byte) {
		frame, err := DecodeFrame(encoded)
		if err == nil {
			canonical, encodeErr := EncodeFrame(frame)
			if encodeErr != nil || !bytes.Equal(encoded, canonical) {
				t.Fatal("noncanonical frame accepted")
			}
		}
	})
}
