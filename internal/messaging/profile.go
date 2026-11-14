package messaging

import (
	"encoding/binary"
	"io"
	"time"

	"whatsup/internal/profile"
)

// A profile is a version byte and a bounded UTF-8 name. Empty explicitly means
// sharing is disabled or the sender cleared its name. There are no URLs, image
// bytes, paths, claims of trust, or peer-selectable contact IDs in this packet.
const profileVersion byte = 1

func encodeProfile(name string) ([]byte, error) {
	if profile.ValidateName(name) != nil {
		return nil, errProtocol
	}
	wire := []byte{profilePacket, profileVersion}
	wire = binary.BigEndian.AppendUint16(wire, uint16(len(name)))
	wire = append(wire, name...)
	return wire, nil
}

func readProfile(reader io.Reader) (string, error) {
	var header [3]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return "", errProtocol
	}
	length := int(binary.BigEndian.Uint16(header[1:]))
	if header[0] != profileVersion || length > profile.MaxNameBytes {
		return "", errProtocol
	}
	name := make([]byte, length)
	if _, err := io.ReadFull(reader, name); err != nil || profile.ValidateName(string(name)) != nil {
		return "", errProtocol
	}
	return string(name), nil
}

// Only changed names use the bounded write budget. Repeated identical frames
// remain idempotent in storage and do not cause repeated ciphertext writes.
type profileBudget struct {
	start time.Time
	count int
	seen  bool
	last  string
}

func (b *profileBudget) accept(name string, now time.Time) bool {
	if b.seen && b.last == name {
		return true
	}
	if b.start.IsZero() || now.Sub(b.start) >= 10*time.Second {
		b.start, b.count = now, 0
	}
	if b.count >= 5 {
		return false
	}
	b.count++
	b.seen, b.last = true, name
	return true
}
