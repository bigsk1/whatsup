package messaging

import (
	"crypto/sha256"
	"encoding/binary"
	"io"
	"time"

	"whatsup/internal/media"
)

const avatarPacket byte = 8
const maxAvatarRevision = uint64(1<<53 - 1)

type avatarFrame struct {
	revision uint64
	data     []byte
}

func encodeAvatar(frame avatarFrame) ([]byte, error) {
	if frame.revision == 0 || frame.revision > maxAvatarRevision || len(frame.data) > media.MaxAvatarBytes {
		return nil, errProtocol
	}
	if len(frame.data) != 0 {
		if _, err := media.AvatarType(frame.data); err != nil {
			return nil, errProtocol
		}
	}
	wire := []byte{avatarPacket, 1}
	wire = binary.BigEndian.AppendUint64(wire, frame.revision)
	wire = binary.BigEndian.AppendUint32(wire, uint32(len(frame.data)))
	return append(wire, frame.data...), nil
}

func readAvatar(reader io.Reader) (avatarFrame, error) {
	var header [13]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return avatarFrame{}, errProtocol
	}
	frame := avatarFrame{revision: binary.BigEndian.Uint64(header[1:9])}
	length := binary.BigEndian.Uint32(header[9:])
	if header[0] != 1 || frame.revision == 0 || frame.revision > maxAvatarRevision || length > media.MaxAvatarBytes {
		return avatarFrame{}, errProtocol
	}
	frame.data = make([]byte, int(length))
	if _, err := io.ReadFull(reader, frame.data); err != nil {
		return avatarFrame{}, errProtocol
	}
	if length != 0 {
		if _, err := media.AvatarType(frame.data); err != nil {
			return avatarFrame{}, errProtocol
		}
	}
	return frame, nil
}

type avatarBudget struct {
	start    time.Time
	count    int
	revision uint64
	digest   [32]byte
}

func (b *avatarBudget) accept(frame avatarFrame, now time.Time) bool {
	digest := sha256.Sum256(frame.data)
	if frame.revision <= b.revision {
		return frame.revision < b.revision || digest == b.digest
	}
	if b.start.IsZero() || now.Sub(b.start) >= 10*time.Second {
		b.start, b.count = now, 0
	}
	if b.count >= 5 {
		return false
	}
	b.revision, b.digest = frame.revision, digest
	b.count++
	return true
}
