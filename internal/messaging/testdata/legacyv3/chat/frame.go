// Historical compatibility fixture from WhatsUp commit 59a235d.
// The reviewed previous implementation is retained without behavior changes.

// Package chat implements bounded plaintext framing inside an authenticated,
// encrypted transport and a durable conversation domain. It does not open a
// socket, authenticate a claimed peer, or complete mutual pairing. Construct it
// only after reciprocal approval has been persisted, and supply the actual peer
// identity from the authenticated transport on every send, receive, and ack.
package chat

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ProtocolVersion = 1
	MaxTextBytes    = 16 << 10
	// JSON escaping can expand a permitted text byte to six bytes. The wire
	// bound accommodates that expansion while still rejecting oversized frames
	// before allocating their body.
	MaxFrameBytes = 128 << 10
	MessageFrame  = "message"
	AckFrame      = "ack"
)

var (
	ErrFrame = errors.New("invalid or oversized chat frame")
	ErrText  = errors.New("message must contain bounded, valid display text")
)

// Frame contains no identities or credentials: identity comes exclusively from
// the authenticated transport. Text is always inert display text. A future
// renderer must escape it and must not enable HTML, remote images, or execution.
type Frame struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Text    string `json:"text,omitempty"`
}

func validID(id string) bool {
	if len(id) != base64.RawURLEncoding.EncodedLen(16) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(id)
	return err == nil && len(decoded) == 16 && base64.RawURLEncoding.EncodeToString(decoded) == id
}

func validateText(text string) error {
	if len(text) == 0 || len(text) > MaxTextBytes || !utf8.ValidString(text) {
		return ErrText
	}
	for len(text) != 0 {
		r, size := utf8.DecodeRuneInString(text)
		// Unicode 18.0 defines exactly these three RGI emoji tag sequences.
		// Accepting arbitrary tag letters, even after a black flag, would allow
		// invisible ASCII payloads. Consume only a complete recognized flag.
		if r == '\U0001f3f4' {
			if length := emojiTagFlagLength(text); length != 0 {
				text = text[length:]
				continue
			}
		}
		text = text[size:]
		// These format characters support ordinary text shaping and optional
		// hyphenation. They do not change bidi direction.
		// Keep them verbatim; the renderer isolates each message from UI labels.
		if r == '\u00ad' || r == '\u200c' || r == '\u200d' {
			continue
		}
		// Continue rejecting direction overrides/embeddings, other format
		// controls and invisible fillers that can disguise interface text.
		// Ordinary RTL letters, combining accents and variation selectors are
		// valid without permitting peer-controlled bidi formatting.
		if (unicode.IsControl(r) && r != '\n' && r != '\t') ||
			unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) ||
			unicode.Is(unicode.Zp, r) || unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) {
			return ErrText
		}
	}
	return nil
}

func emojiTagFlagLength(text string) int {
	for _, flag := range [...]string{
		"\U0001f3f4\U000e0067\U000e0062\U000e0065\U000e006e\U000e0067\U000e007f", // England
		"\U0001f3f4\U000e0067\U000e0062\U000e0073\U000e0063\U000e0074\U000e007f", // Scotland
		"\U0001f3f4\U000e0067\U000e0062\U000e0077\U000e006c\U000e0073\U000e007f", // Wales
	} {
		if strings.HasPrefix(text, flag) {
			return len(flag)
		}
	}
	return 0
}

func (frame Frame) Validate() error {
	if frame.Version != ProtocolVersion || !validID(frame.ID) {
		return ErrFrame
	}
	switch frame.Type {
	case MessageFrame:
		return validateText(frame.Text)
	case AckFrame:
		if frame.Text == "" {
			return nil
		}
	}
	return ErrFrame
}

func EncodeFrame(frame Frame) ([]byte, error) {
	if err := frame.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(frame)
	if err != nil || len(encoded) > MaxFrameBytes {
		return nil, ErrFrame
	}
	return encoded, nil
}

// DecodeFrame accepts the protocol's one canonical JSON representation. Requiring
// exact re-encoding also rejects duplicate keys, unknown fields, alternate key
// casing, invalid UTF-8, trailing values, and ambiguous identity/ID substitution.
func DecodeFrame(encoded []byte) (Frame, error) {
	var frame Frame
	if len(encoded) == 0 || len(encoded) > MaxFrameBytes || !utf8.Valid(encoded) {
		return frame, ErrFrame
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame); err != nil {
		return Frame{}, ErrFrame
	}
	canonical, err := EncodeFrame(frame)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return Frame{}, ErrFrame
	}
	return frame, nil
}

// ReadFrame consumes a big-endian uint32 byte count followed by canonical JSON.
// A real connection adapter must set bounded read/write deadlines; a generic
// io.Reader cannot enforce a network timeout or authenticate its source.
func ReadFrame(reader io.Reader) (Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 || length > MaxFrameBytes {
		return Frame{}, ErrFrame
	}
	encoded := make([]byte, int(length))
	if _, err := io.ReadFull(reader, encoded); err != nil {
		return Frame{}, err
	}
	return DecodeFrame(encoded)
}

func WriteFrame(writer io.Writer, frame Frame) error {
	encoded, err := EncodeFrame(frame)
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(encoded)))
	if written, err := writer.Write(header[:]); err != nil {
		return err
	} else if written != len(header) {
		return io.ErrShortWrite
	}
	if written, err := writer.Write(encoded); err != nil {
		return err
	} else if written != len(encoded) {
		return io.ErrShortWrite
	}
	return nil
}
