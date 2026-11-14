package messaging

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"whatsup/internal/chat"
)

type headerOnlyReader struct {
	header   *bytes.Reader
	bodyRead bool
}

func (r *headerOnlyReader) Read(p []byte) (int, error) {
	if r.header.Len() > 0 {
		return r.header.Read(p)
	}
	r.bodyRead = true
	return 0, errors.New("unexpected body allocation/read")
}

func TestMessagingPacketRejectsUnknownTypesAndOversizeBeforeBodyRead(t *testing.T) {
	for _, kind := range []byte{0, 5, 255} {
		reader := bytes.NewReader([]byte{kind, 1, 2, 3})
		if _, err := readPacket(reader); !errors.Is(err, errProtocol) || reader.Len() != 3 {
			t.Fatal("unknown packet type consumed payload or was accepted")
		}
	}
	for _, length := range []uint32{0, chat.MaxFrameBytes + 1, ^uint32(0)} {
		wire := []byte{dataPacket}
		wire = binary.BigEndian.AppendUint32(wire, length)
		reader := &headerOnlyReader{header: bytes.NewReader(wire)}
		if _, err := readPacket(reader); !errors.Is(err, chat.ErrFrame) || reader.bodyRead {
			t.Fatal("oversized packet read body before rejecting its length")
		}
	}
}

func TestMessagingPacketConsumesOneFrameWithoutSwallowingHeartbeat(t *testing.T) {
	frame := chat.Frame{Version: chat.ProtocolVersion, Type: chat.MessageFrame, ID: "AQEBAQEBAQEBAQEBAQEBAQ", Text: "synthetic inert packet text"}
	var wire bytes.Buffer
	wire.WriteByte(dataPacket)
	if err := chat.WriteFrame(&wire, frame); err != nil {
		t.Fatal(err)
	}
	nonce := [16]byte{1, 2, 3}
	wire.WriteByte(pingPacket)
	wire.Write(nonce[:])
	wire.WriteByte(pongPacket)
	wire.Write(nonce[:])
	got, err := readPacket(&wire)
	if err != nil || got.kind != dataPacket || got.frame != frame {
		t.Fatal("valid data packet changed")
	}
	for _, kind := range []byte{pingPacket, pongPacket} {
		got, err := readPacket(&wire)
		if err != nil || got.kind != kind || got.nonce != nonce {
			t.Fatal("data frame swallowed or changed neighboring heartbeat")
		}
	}
	if wire.Len() != 0 {
		t.Fatal("packet reader left unexpected bytes")
	}
}

func TestMessagingPacketRejectsTruncatedHeartbeatAndAmbiguousChat(t *testing.T) {
	for _, kind := range []byte{pingPacket, pongPacket} {
		for length := 0; length < 16; length++ {
			wire := append([]byte{kind}, make([]byte, length)...)
			if _, err := readPacket(bytes.NewReader(wire)); err == nil {
				t.Fatal("partial heartbeat accepted")
			}
		}
	}
	for _, payload := range []string{`{"version":1,"type":"ack","id":"AQEBAQEBAQEBAQEBAQEBAQ","text":"forbidden"}`, `{"version":1,"type":"message","id":"AQEBAQEBAQEBAQEBAQEBAQ","text":"a","text":"b"}`, `{"version":1,"type":"message","id":"AQEBAQEBAQEBAQEBAQEBAQ","text":"unsafe\u202e"}`} {
		wire := []byte{dataPacket}
		wire = binary.BigEndian.AppendUint32(wire, uint32(len(payload)))
		wire = append(wire, payload...)
		if _, err := readPacket(bytes.NewReader(wire)); err == nil {
			t.Fatal("ambiguous or display-spoofing chat escaped packet validation")
		}
	}
	if _, err := readPacket(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatal("empty stream was not rejected")
	}
}
