package messaging

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"

	"whatsup/internal/profile"
)

func TestProfilePacketIsBoundedCanonicalAndSeparateFromHeartbeat(t *testing.T) {
	for _, name := range []string{"", "Synthetic Alice", "مرحبا", strings.Repeat("é", 64)} {
		wire, err := encodeProfile(name)
		if err != nil || len(wire) > profile.MaxNameBytes+4 {
			t.Fatal("valid bounded name could not be framed")
		}
		wire = append(wire, pingPacket)
		wire = append(wire, make([]byte, 16)...)
		reader := bytes.NewReader(wire)
		p, err := readPacket(reader)
		if err != nil || p.kind != profilePacket || p.name != name {
			t.Fatal("profile packet changed the validated name")
		}
		if next, err := readPacket(reader); err != nil || next.kind != pingPacket || reader.Len() != 0 {
			t.Fatal("profile framing consumed the next heartbeat")
		}
	}
	for _, name := range []string{" leading", "trailing ", "line\nbreak", "spoof\u202e", "fill\u3164", string([]byte{0xff}), strings.Repeat("x", 65)} {
		if _, err := encodeProfile(name); !errors.Is(err, errProtocol) {
			t.Fatal("invalid local profile could be encoded")
		}
		wire := []byte{profilePacket, profileVersion}
		wire = binary.BigEndian.AppendUint16(wire, uint16(len(name)))
		wire = append(wire, name...)
		if _, err := readPacket(bytes.NewReader(wire)); err == nil {
			t.Fatal("invalid peer profile escaped validation")
		}
	}
}

func TestProfilePacketRejectsLengthVersionAndTruncationBeforeAllocation(t *testing.T) {
	for _, length := range []uint16{profile.MaxNameBytes + 1, 65535} {
		header := []byte{profilePacket, profileVersion}
		header = binary.BigEndian.AppendUint16(header, length)
		reader := &headerOnlyReader{header: bytes.NewReader(header)}
		if _, err := readPacket(reader); !errors.Is(err, errProtocol) || reader.bodyRead {
			t.Fatal("oversized profile read its body")
		}
	}
	for _, wire := range [][]byte{{profilePacket}, {profilePacket, profileVersion}, {profilePacket, profileVersion, 0}, {profilePacket, profileVersion, 0, 1}, {profilePacket, 2, 0, 0}} {
		if _, err := readPacket(bytes.NewReader(wire)); err == nil {
			t.Fatal("truncated or unknown-version profile accepted")
		}
	}
}

func TestProfileChangeBudgetIgnoresDuplicatesAndBoundsWrites(t *testing.T) {
	var budget profileBudget
	now := time.Now()
	for index := range 5 {
		name := strings.Repeat("x", index+1)
		if !budget.accept(name, now) {
			t.Fatal("ordinary bounded updates were refused")
		}
		for range 20 {
			if !budget.accept(name, now) {
				t.Fatal("idempotent duplicate consumed another write budget")
			}
		}
	}
	if budget.accept("sixth", now) || !budget.accept("after window", now.Add(10*time.Second)) {
		t.Fatal("profile write budget was not bounded and renewable")
	}
}

func TestExplicitProfileChangeWakesOnlyExistingSessionAndWithdrawingClearsName(t *testing.T) {
	m, server, client, conversation, _ := exchangeFixture(t)
	if _, err := m.application.SetProfile("Synthetic Alice", false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.exchange(ctx, server, conversation) }()
	consumeInitialProfile(t, client)
	for _, change := range []struct {
		share bool
		want  string
	}{{true, "Synthetic Alice"}, {false, ""}} {
		if _, err := m.application.SetProfile("Synthetic Alice", change.share); err != nil {
			t.Fatal(err)
		}
		m.MetadataChanged()
		got, err := readPacket(client)
		if err != nil || got.kind != profilePacket || got.name != change.want {
			t.Fatal("profile sharing change was not reflected in the existing stream")
		}
	}
	cancel()
	waitExchange(t, result)
}

func TestCancellationInterruptsInitialProfileWriteAndJoinsReader(t *testing.T) {
	m, server, _, conversation, fault := exchangeFixture(t)
	if _, err := m.application.SetProfile("Synthetic Private Profile", true); err != nil {
		t.Fatal(err)
	}
	fault.blockWrite.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- m.exchange(ctx, server, conversation) }()
	// Block the first actual TLS application write after authentication. The
	// exchange must close it and join its reader without the normal write timeout.
	select {
	case <-fault.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("initial profile write did not reach the transport")
	}
	cancel()
	waitExchange(t, result)
	if server.WithLivePeer(m.record.Peer, func() error { return nil }) == nil {
		t.Fatal("canceled profile write retained live authority")
	}
}
