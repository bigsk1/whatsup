package chat

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
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
		0x0085, 0x009b, 0x034f, 0x061c, 0x115f, 0x1160,
		0x17b4, 0x17b5, 0x180e, 0x200b, 0x200e, 0x200f,
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

func TestTextShapingAndEmojiRemainVerbatimAcrossWire(t *testing.T) {
	for name, text := range map[string]string{
		"family emoji":        "Family: 👨\u200d👩\u200d👧\u200d👦",
		"heart on fire":       "Love ❤️\u200d🔥",
		"rainbow flag":        "Flag 🏳️\u200d🌈",
		"England flag":        "Flag 🏴\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f",
		"Scotland flag":       "Flag 🏴\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f",
		"Wales flag":          "Flag 🏴\U000e0067\U000e0062\U000e0077\U000e006c\U000e0073\U000e007f",
		"bare black flag":     "Flag 🏴 without tags",
		"Persian non-joiner":  "می\u200cروم",
		"Indic joiner":        "क्\u200dष",
		"Indic non-joiner":    "क्\u200cष",
		"optional hyphen":     "copy co\u00adoperate from a webpage",
		"variation selectors": "Text \u2764\ufe0e and emoji \u2764\ufe0f",
	} {
		t.Run(name, func(t *testing.T) {
			frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: text}
			var stream bytes.Buffer
			if err := WriteFrame(&stream, frame); err != nil {
				t.Fatalf("legitimate Unicode text rejected: %v", err)
			}
			if received, err := ReadFrame(&stream); err != nil || received.Text != text {
				t.Fatalf("Unicode text changed across the wire: %v", err)
			}
			// Exercise a peer's bytes independently of our encoder as well.
			encoded, err := json.Marshal(frame)
			if err != nil {
				t.Fatal(err)
			}
			if received, err := DecodeFrame(encoded); err != nil || received.Text != text {
				t.Fatalf("legitimate peer Unicode text rejected or changed: %v", err)
			}
		})
	}
}

func TestEmojiTagsCannotCarryArbitraryOrMalformedHiddenText(t *testing.T) {
	tags := func(ascii string) string {
		var encoded strings.Builder
		for _, character := range ascii {
			encoded.WriteRune('\U000e0000' + character)
		}
		return encoded.String()
	}
	const blackFlag, cancel = "\U0001f3f4", "\U000e007f"
	england := blackFlag + tags("gbeng") + cancel
	bad := map[string]string{
		"hidden ASCII without flag": tags("synthetic text") + cancel,
		"hidden ASCII after flag":   blackFlag + tags("synthetic text") + cancel,
		"no flag base":              tags("gbeng") + cancel,
		"wrong flag base":           "😀" + tags("gbeng") + cancel,
		"no cancel tag":             blackFlag + tags("gbeng"),
		"empty payload":             blackFlag + cancel,
		"partial payload":           blackFlag + tags("gbe") + cancel,
		"unknown subdivision":       blackFlag + tags("gbabc") + cancel,
		"uppercase payload":         blackFlag + tags("GBENG") + cancel,
		"numeric payload":           blackFlag + tags("12345") + cancel,
		"interrupted payload":       blackFlag + tags("gb") + "x" + tags("eng") + cancel,
		"joiner in payload":         blackFlag + tags("gb") + "\u200d" + tags("eng") + cancel,
		"nested flag":               blackFlag + tags("gb") + blackFlag + tags("eng") + cancel,
		"extra variation selector":  blackFlag + "\ufe0f" + tags("gbeng") + cancel,
		"extra payload":             blackFlag + tags("gbengx") + cancel,
		"trailing hidden payload":   england + tags("synthetic") + cancel,
		"trailing cancel":           england + cancel,
		"overlong payload":          blackFlag + tags(strings.Repeat("a", 1000)) + cancel,
	}
	// Every tag character, including CANCEL TAG, is invalid outside the exact
	// complete sequences. Exercise peer bytes independently of local validation.
	for character := '\U000e0020'; character <= '\U000e007f'; character++ {
		bad[fmt.Sprintf("stray U+%X", character)] = "visible" + string(character) + "text"
	}
	for name, text := range bad {
		t.Run(name, func(t *testing.T) {
			frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: text}
			if _, err := EncodeFrame(frame); !errors.Is(err, ErrText) {
				t.Fatal("malformed or arbitrary hidden tag payload accepted locally")
			}
			wire, err := json.Marshal(frame)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeFrame(wire); err == nil {
				t.Fatal("malformed or arbitrary hidden tag payload accepted from peer")
			}
		})
	}
	for _, text := range []string{
		england + england,
		"مرحبا " + england + " hello",
		strings.Repeat("x", MaxTextBytes-len(england)) + england,
	} {
		frame := Frame{Version: ProtocolVersion, Type: MessageFrame, ID: syntheticID, Text: text}
		wire, err := EncodeFrame(frame)
		if err != nil {
			t.Fatal("complete recognized flags rejected at message boundaries")
		}
		if got, err := DecodeFrame(wire); err != nil || got.Text != text {
			t.Fatal("complete recognized flags changed across the wire")
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
