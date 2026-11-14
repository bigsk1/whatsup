package transfer

import (
	"encoding/base64"
	"encoding/binary"
	"io"
)

type Kind byte

const (
	OfferFrame Kind = iota + 1
	AcceptFrame
	CheckpointFrame
	ChunkFrame
	FinishFrame
	CompleteFrame
	PauseFrame
	DeclineFrame
	FailureFrame
	WithdrawFrame
	WithdrawAckFrame
)
const (
	ProtocolVersion byte = 1
	MaxFrameBytes        = ChunkBytes + 74
)
const (
	ReasonNone byte = iota
	ReasonPaused
	ReasonDeclined
	ReasonCapacity
	ReasonIntegrity
	ReasonUnavailable
	ReasonRelayApproval
	ReasonCompleted
)

type Frame struct {
	Kind      Kind
	ID        string
	SHA256    [32]byte
	Offer     Offer
	NextChunk uint32
	Index     uint32
	Data      []byte
	Reason    byte
	Credit    [16]byte
}

func (Frame) String() string   { return "[private WhatsUp attachment frame]" }
func (Frame) GoString() string { return "[private WhatsUp attachment frame]" }

func (f Frame) Validate() error {
	if !ValidID(f.ID) || f.SHA256 == [32]byte{} {
		return ErrProtocol
	}
	needsCredit := f.Kind == AcceptFrame || f.Kind == CheckpointFrame || f.Kind == ChunkFrame || f.Kind == FinishFrame
	if needsCredit == (f.Credit == [16]byte{}) {
		return ErrProtocol
	}
	if f.Kind != OfferFrame && f.Offer != (Offer{}) || f.Kind != ChunkFrame && (f.Index != 0 || len(f.Data) != 0) ||
		f.Kind != AcceptFrame && f.Kind != CheckpointFrame && f.NextChunk != 0 ||
		f.Kind != PauseFrame && f.Kind != DeclineFrame && f.Kind != FailureFrame && f.Kind != WithdrawAckFrame && f.Reason != 0 {
		return ErrProtocol
	}
	switch f.Kind {
	case OfferFrame:
		if f.Offer.Validate() != nil || f.Offer.ID != f.ID || f.Offer.SHA256 != f.SHA256 {
			return ErrProtocol
		}
	case AcceptFrame, CheckpointFrame:
		if f.NextChunk > uint32((MaxFileBytes+ChunkBytes-1)/ChunkBytes) {
			return ErrProtocol
		}
	case ChunkFrame:
		if len(f.Data) == 0 || len(f.Data) > ChunkBytes || f.Index >= uint32((MaxFileBytes+ChunkBytes-1)/ChunkBytes) {
			return ErrProtocol
		}
	case FinishFrame, CompleteFrame:
	case WithdrawFrame:
	case WithdrawAckFrame:
		if f.Reason != ReasonDeclined && f.Reason != ReasonCompleted {
			return ErrProtocol
		}
	case PauseFrame:
		if f.Reason != ReasonPaused && f.Reason != ReasonUnavailable && f.Reason != ReasonRelayApproval {
			return ErrProtocol
		}
	case DeclineFrame:
		if f.Reason != ReasonDeclined {
			return ErrProtocol
		}
	case FailureFrame:
		if f.Reason != ReasonCapacity && f.Reason != ReasonIntegrity && f.Reason != ReasonUnavailable {
			return ErrProtocol
		}
	default:
		return ErrProtocol
	}
	return nil
}

func EncodeFrame(f Frame) ([]byte, error) {
	if f.Validate() != nil {
		return nil, ErrProtocol
	}
	length := 50
	switch f.Kind {
	case OfferFrame:
		length += 11 + len(f.Offer.Name) + len(f.Offer.MediaType)
	case AcceptFrame, CheckpointFrame:
		length += 20
	case ChunkFrame:
		length += 24 + len(f.Data)
	case FinishFrame:
		length += 16
	case PauseFrame, DeclineFrame, FailureFrame, WithdrawAckFrame:
		length++
	}
	encoded := make([]byte, length)
	encoded[0], encoded[1] = ProtocolVersion, byte(f.Kind)
	id, _ := base64.RawURLEncoding.Strict().DecodeString(f.ID)
	copy(encoded[2:18], id)
	copy(encoded[18:50], f.SHA256[:])
	data := encoded[50:]
	if f.Credit != [16]byte{} {
		copy(data[:16], f.Credit[:])
		data = data[16:]
	}
	switch f.Kind {
	case OfferFrame:
		binary.BigEndian.PutUint64(data[:8], f.Offer.Size)
		binary.BigEndian.PutUint16(data[8:10], uint16(len(f.Offer.Name)))
		data[10] = byte(len(f.Offer.MediaType))
		copy(data[11:], f.Offer.Name)
		copy(data[11+len(f.Offer.Name):], f.Offer.MediaType)
	case AcceptFrame, CheckpointFrame:
		binary.BigEndian.PutUint32(data, f.NextChunk)
	case ChunkFrame:
		binary.BigEndian.PutUint32(data[:4], f.Index)
		binary.BigEndian.PutUint32(data[4:8], uint32(len(f.Data)))
		copy(data[8:], f.Data)
	case PauseFrame, DeclineFrame, FailureFrame, WithdrawAckFrame:
		data[0] = f.Reason
	}
	return encoded, nil
}

func DecodeFrame(encoded []byte) (Frame, error) {
	if len(encoded) < 50 || len(encoded) > MaxFrameBytes || encoded[0] != ProtocolVersion {
		return Frame{}, ErrProtocol
	}
	f := Frame{Kind: Kind(encoded[1]), ID: base64.RawURLEncoding.EncodeToString(encoded[2:18])}
	copy(f.SHA256[:], encoded[18:50])
	data := encoded[50:]
	if f.Kind == AcceptFrame || f.Kind == CheckpointFrame || f.Kind == ChunkFrame || f.Kind == FinishFrame {
		if len(data) < 16 {
			return Frame{}, ErrProtocol
		}
		copy(f.Credit[:], data[:16])
		data = data[16:]
	}
	switch f.Kind {
	case OfferFrame:
		if len(data) < 11 {
			return Frame{}, ErrProtocol
		}
		name, media := int(binary.BigEndian.Uint16(data[8:10])), int(data[10])
		if len(data) != 11+name+media {
			return Frame{}, ErrProtocol
		}
		f.Offer = Offer{ID: f.ID, SHA256: f.SHA256, Size: binary.BigEndian.Uint64(data[:8]), Name: string(data[11 : 11+name]), MediaType: string(data[11+name:])}
	case AcceptFrame, CheckpointFrame:
		if len(data) != 4 {
			return Frame{}, ErrProtocol
		}
		f.NextChunk = binary.BigEndian.Uint32(data)
	case ChunkFrame:
		if len(data) < 8 || int(binary.BigEndian.Uint32(data[4:8])) != len(data)-8 {
			return Frame{}, ErrProtocol
		}
		f.Index, f.Data = binary.BigEndian.Uint32(data[:4]), append([]byte(nil), data[8:]...)
	case FinishFrame, CompleteFrame, WithdrawFrame:
		if len(data) != 0 {
			return Frame{}, ErrProtocol
		}
	case PauseFrame, DeclineFrame, FailureFrame, WithdrawAckFrame:
		if len(data) != 1 {
			return Frame{}, ErrProtocol
		}
		f.Reason = data[0]
	default:
		return Frame{}, ErrProtocol
	}
	if f.Validate() != nil {
		return Frame{}, ErrProtocol
	}
	return f, nil
}

func ReadFrame(reader io.Reader) (Frame, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return Frame{}, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length < 50 || length > MaxFrameBytes {
		return Frame{}, ErrProtocol
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(reader, data); err != nil {
		return Frame{}, err
	}
	return DecodeFrame(data)
}

func WriteFrame(writer io.Writer, f Frame) error {
	encoded, err := EncodeFrame(f)
	if err != nil {
		return err
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(encoded)))
	if n, err := writer.Write(header[:]); err != nil {
		return err
	} else if n != len(header) {
		return io.ErrShortWrite
	}
	if n, err := writer.Write(encoded); err != nil {
		return err
	} else if n != len(encoded) {
		return io.ErrShortWrite
	}
	return nil
}
