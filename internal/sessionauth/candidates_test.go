package sessionauth

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"whatsup/internal/identity"
)

func connectCandidates(t *testing.T, a, b Options, associations [][32]byte) (result, result) {
	t.Helper()
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
	done := make(chan result, 1)
	go func() { s, e := AuthenticateCandidates(ctx, left, a, associations); done <- result{s, e} }()
	s, e := Authenticate(ctx, right, b)
	other := <-done
	if other.session != nil {
		t.Cleanup(func() { _ = other.session.Close() })
	}
	if s != nil {
		t.Cleanup(func() { _ = s.Close() })
	}
	return other, result{s, e}
}

func TestCandidateListenerProvesOnlyExactNegotiatedAssociation(t *testing.T) {
	for _, second := range []bool{false, true} {
		a, b := optionsPair(t)
		associations := [][32]byte{a.Association, {2}}
		if second {
			b.Association = associations[1]
		}
		left, right := connectCandidates(t, a, b, associations)
		if left.err != nil || right.err != nil || left.session == nil || right.session == nil {
			t.Fatal("explicit candidate failed pinned TLS")
		}
		for _, s := range []*Session{left.session, right.session} {
			local, peer := a.Local.PublicKey(), a.Peer
			if s == right.session {
				local, peer = b.Local.PublicKey(), b.Peer
			}
			if !s.Matches(local, peer, a.ContactID, b.Association) || s.Capabilities() != (Capabilities{WireVersion: 4, Attachments: true, Control: true, Withdrawals: true, Avatars: true}) {
				t.Fatal("candidate proof or capabilities did not follow exact TLS negotiation")
			}
			other := associations[1]
			if second {
				other = associations[0]
			}
			if s.Matches(local, peer, a.ContactID, other) {
				t.Fatal("one negotiated route authorized the other route")
			}
		}
		written := make(chan error, 1)
		go func() { _, e := left.session.Write([]byte("synthetic route text")); written <- e }()
		data := make([]byte, len("synthetic route text"))
		if _, err := io.ReadFull(right.session, data); err != nil || string(data) != "synthetic route text" || <-written != nil {
			t.Fatal("selected candidate did not carry authenticated data")
		}
		proof := *left.session
		_ = left.session.Close()
		if proof.WithLivePeer(a.Peer, func() error { return nil }) == nil {
			t.Fatal("candidate proof survived session closure")
		}
	}
}

func TestCandidateListenerDoesNotGrantWildcardAssociationOrIdentity(t *testing.T) {
	for _, kind := range []string{"association", "contact", "server-key", "client-key"} {
		t.Run(kind, func(t *testing.T) {
			a, b := optionsPair(t)
			associations := [][32]byte{a.Association, {2}}
			unknown, _ := identity.Generate()
			switch kind {
			case "association":
				b.Association = [32]byte{3}
			case "contact":
				b.ContactID = "c3ludGhldGljLWlkLTAwMg"
			case "server-key":
				b.Peer = unknown.PublicKey()
			case "client-key":
				b.Local = unknown
			}
			left, right := connectCandidates(t, a, b, associations)
			if left.session != nil || left.err == nil {
				t.Fatal("server admitted an unapproved association or identity")
			}
			if kind != "client-key" && (right.session != nil || right.err == nil) {
				t.Fatal("unapproved server or association gained a session")
			}
			if right.session != nil {
				_ = right.session.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := right.session.Read(make([]byte, 1)); err == nil {
					t.Fatal("rejected client received application data")
				}
			}
			if (kind == "association" || kind == "contact") && errors.Is(left.err, ErrIdentityChanged) {
				t.Fatal("route mismatch was misclassified as changed identity")
			}
		})
	}
}

func TestCandidateListenerKeepsPreviousWireCapabilitiesOnSecondAssociation(t *testing.T) {
	a, b := optionsPair(t)
	associations := [][32]byte{a.Association, {2}}
	b.Association = associations[1]
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	left, right := net.Pipe()
	defer right.Close()
	done := make(chan result, 1)
	go func() { s, e := AuthenticateCandidates(ctx, left, a, associations); done <- result{s, e} }()
	cert, err := newCertificate(b.Local, time.Now(), time.Now().Add(MaxLifetime))
	if err != nil {
		t.Fatal(err)
	}
	alpn := previousProtocol + "." + b.ContactID + "." + base64.RawURLEncoding.EncodeToString(b.Association[:])
	client := tls.Client(right, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{cert}, NextProtos: []string{alpn}})
	_ = right.SetDeadline(time.Now().Add(time.Second))
	if err := client.HandshakeContext(ctx); err != nil {
		t.Fatal("previous wire failed exact second association", err)
	}
	got := <-done
	if got.err != nil || got.session == nil {
		t.Fatal("previous wire did not complete pinned server authentication")
	}
	defer got.session.Close()
	if !got.session.Matches(a.Local.PublicKey(), a.Peer, a.ContactID, b.Association) || got.session.Matches(a.Local.PublicKey(), a.Peer, a.ContactID, a.Association) || got.session.Capabilities() != (Capabilities{WireVersion: 3}) {
		t.Fatal("second association upgraded previous wire capabilities or proved wrong route")
	}
}

func TestCandidateListenerRejectsInvalidSetsAndClientUseBeforeHandshake(t *testing.T) {
	for _, kind := range []string{"nil", "empty", "zero", "duplicate", "too-many", "wrong-first", "client"} {
		t.Run(kind, func(t *testing.T) {
			a, _ := optionsPair(t)
			associations := [][32]byte{a.Association, {2}}
			switch kind {
			case "nil":
				associations = nil
			case "empty":
				associations = [][32]byte{}
			case "zero":
				associations[1] = [32]byte{}
			case "duplicate":
				associations[1] = associations[0]
			case "too-many":
				associations = append(associations, [32]byte{3})
			case "wrong-first":
				associations[0] = [32]byte{3}
			case "client":
				a.Server = false
			}
			left, right := net.Pipe()
			defer right.Close()
			s, err := AuthenticateCandidates(context.Background(), left, a, associations)
			if s != nil || !errors.Is(err, ErrAuthentication) {
				t.Fatal("invalid candidate set started authentication")
			}
			if _, err := right.Write([]byte{1}); err == nil {
				t.Fatal("rejected candidate helper retained raw connection")
			}
		})
	}
}

func TestCandidateProtocol2DiagnosticRequiresExactPinnedAssociation(t *testing.T) {
	for _, index := range []int{0, 1} {
		for _, kind := range []string{"valid", "wrong-association", "wrong-contact", "wrong-key"} {
			t.Run(fmt.Sprintf("association=%d/%s", index, kind), func(t *testing.T) {
				current, old := optionsPair(t)
				associations := [][32]byte{current.Association, {2}}
				old.Association = associations[index]
				switch kind {
				case "wrong-association":
					old.Association = [32]byte{3}
				case "wrong-contact":
					old.ContactID = "c3ludGhldGljLWlkLTAwMg"
				case "wrong-key":
					old.Local, _ = identity.Generate()
				}
				alpn := diagnosticProtocol + "." + old.ContactID + "." + base64.RawURLEncoding.EncodeToString(old.Association[:])
				got, peer, handshakeErr := rawProtocolPeer(t, current, associations, protocolPeerConfig(t, old, []string{alpn}))
				want := ErrAuthentication
				if kind == "valid" {
					want = ErrIncompatible
					if handshakeErr != nil || !peer.ConnectionState().HandshakeComplete || peer.ConnectionState().NegotiatedProtocol != alpn {
						t.Fatalf("candidate diagnostic did not finish exact pinned TLS: %v", handshakeErr)
					}
				} else if kind == "wrong-key" {
					want = ErrIdentityChanged
				}
				if got.session != nil || !errors.Is(got.err, want) {
					t.Fatalf("candidate diagnostic gained authority or wrong result: %v, want %v", got.err, want)
				}
				assertProtocolPeerClosed(t, peer)
			})
		}
	}
}

func TestCandidateProtocol2DoesNotDisplaceUsableSecondAssociation(t *testing.T) {
	for _, usable := range []string{previousProtocol, protocol, controlProtocol} {
		t.Run(usable, func(t *testing.T) {
			current, peer := optionsPair(t)
			associations := [][32]byte{current.Association, {2}}
			first := "." + peer.ContactID + "." + base64.RawURLEncoding.EncodeToString(associations[0][:])
			second := "." + peer.ContactID + "." + base64.RawURLEncoding.EncodeToString(associations[1][:])
			config := protocolPeerConfig(t, peer, []string{diagnosticProtocol + first, usable + second})
			got, raw, err := rawProtocolPeer(t, current, associations, config)
			if err != nil || got.err != nil || got.session == nil || raw.ConnectionState().NegotiatedProtocol != usable+second {
				t.Fatalf("first candidate's diagnostic displaced usable second candidate: %v / %v", got.err, err)
			}
			if !got.session.Matches(current.Local.PublicKey(), current.Peer, current.ContactID, associations[1]) || got.session.Matches(current.Local.PublicKey(), current.Peer, current.ContactID, associations[0]) {
				t.Fatal("usable second association proved the wrong route")
			}
		})
	}
}
