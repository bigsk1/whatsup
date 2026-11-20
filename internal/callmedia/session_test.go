package callmedia_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"testing"
	"time"

	"whatsup/internal/callmedia"
	"whatsup/internal/identity"
	"whatsup/internal/sessionauth"
)

// authenticatedPair returns the two ends of one real mutually authenticated
// contact session over an in-memory pipe, using disposable identities.
func authenticatedPair(t *testing.T) (inviter, joiner *sessionauth.Session) {
	t.Helper()
	a, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	contact := base64.RawURLEncoding.EncodeToString([]byte("synthetic-id-001"))
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	type outcome struct {
		session *sessionauth.Session
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		s, err := sessionauth.Authenticate(ctx, left, sessionauth.Options{Local: a, Peer: b.PublicKey(), ContactID: contact, Association: [32]byte{1}, Server: true})
		done <- outcome{s, err}
	}()
	joiner, err = sessionauth.Authenticate(ctx, right, sessionauth.Options{Local: b, Peer: a.PublicKey(), ContactID: contact, Association: [32]byte{1}})
	first := <-done
	if err != nil || first.err != nil {
		t.Fatalf("disposable identities did not authenticate: %v / %v", first.err, err)
	}
	t.Cleanup(func() { _ = first.session.Close(); _ = joiner.Close() })
	return first.session, joiner
}

// Media keys taken from the real pinned-identity session open in the right
// direction on the other end, and nowhere else.
func TestMediaKeysFromARealAuthenticatedSession(t *testing.T) {
	inviterSession, joinerSession := authenticatedPair(t)
	call := [16]byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 1, 2, 3, 4, 5, 6, 7}
	inviterKeys, err := callmedia.Derive(inviterSession, call, true)
	if err != nil {
		t.Fatal("inviter could not derive call keys from its live session")
	}
	joinerKeys, err := callmedia.Derive(joinerSession, call, false)
	if err != nil {
		t.Fatal("joiner could not derive call keys from its live session")
	}
	now := time.Now()
	for name, pair := range map[string][2]*callmedia.Keys{"inviter to joiner": {inviterKeys, joinerKeys}, "joiner to inviter": {joinerKeys, inviterKeys}} {
		sender, err := callmedia.NewSender(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		receiver, err := callmedia.NewReceiver(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		frame := bytes.Repeat([]byte{0x42}, 80)
		datagram, err := sender.Audio(frame, 960)
		if err != nil {
			t.Fatal(err)
		}
		got, err := receiver.Open(datagram, now)
		if err != nil || len(got.Frames) != 1 || !bytes.Equal(got.Frames[0].Data, frame) {
			t.Fatalf("%s: media did not open on the other end", name)
		}
		// The sender's own receiving side must not be able to open what it sent.
		own, _ := callmedia.NewReceiver(pair[0])
		echo, _ := sender.Audio(frame, 1920)
		if _, err := own.Open(echo, now); !errors.Is(err, callmedia.ErrDatagram) {
			t.Fatalf("%s: a direction's key also opened its own traffic", name)
		}
	}

	// A second session between the same two identities must not share keys.
	otherInviter, _ := authenticatedPair(t)
	foreign, err := callmedia.Derive(otherInviter, call, false)
	if err != nil {
		t.Fatal(err)
	}
	sender, _ := callmedia.NewSender(inviterKeys)
	receiver, _ := callmedia.NewReceiver(foreign)
	datagram, _ := sender.Audio([]byte{1, 2, 3}, 960)
	if _, err := receiver.Open(datagram, now); !errors.Is(err, callmedia.ErrDatagram) {
		t.Fatal("keys from a different session opened this call's media")
	}

	// Keys die with the session: nothing can be derived after it closes.
	_ = inviterSession.Close()
	if keys, err := callmedia.Derive(inviterSession, call, true); keys != nil || !errors.Is(err, callmedia.ErrKeys) {
		t.Fatal("call keys were derived from a closed session")
	}
}
