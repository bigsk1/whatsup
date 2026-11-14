package sessionauth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"whatsup/internal/identity"
)

type result struct {
	session *Session
	err     error
}

func optionsPair(t *testing.T) (Options, Options) {
	t.Helper()
	a, _ := identity.Generate()
	b, _ := identity.Generate()
	id := base64.RawURLEncoding.EncodeToString([]byte("synthetic-id-001"))
	return Options{Local: a, Peer: b.PublicKey(), ContactID: id, Association: [32]byte{1}, Server: true}, Options{Local: b, Peer: a.PublicKey(), ContactID: id, Association: [32]byte{1}}
}

func connect(t *testing.T, a, b Options) (result, result) {
	t.Helper()
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	done := make(chan result, 1)
	go func() { s, e := Authenticate(ctx, left, a); done <- result{s, e} }()
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

func TestSavedIdentitiesReconnectWithoutAnyUserDecision(t *testing.T) {
	a, b := optionsPair(t)
	for range 3 {
		left, right := connect(t, a, b)
		if left.err != nil || right.err != nil {
			t.Fatal("saved identity authentication failed")
		}
		if !left.session.Matches(a.Local.PublicKey(), a.Peer, a.ContactID, a.Association) || left.session.WithLivePeer(a.Peer, func() error { return nil }) != nil {
			t.Fatal("live proof lost exact contact association")
		}
		written := make(chan error, 1)
		go func() { _, e := left.session.Write([]byte("synthetic contact text")); written <- e }()
		data := make([]byte, len("synthetic contact text"))
		if _, e := io.ReadFull(right.session, data); e != nil || string(data) != "synthetic contact text" || <-written != nil {
			t.Fatal("authenticated stream did not carry exact text")
		}
		copyProof := *left.session
		_ = left.session.Close()
		if copyProof.WithLivePeer(a.Peer, func() error { t.Fatal("closed proof admitted operation"); return nil }) == nil {
			t.Fatal("copied proof survived closure")
		}
		_ = right.session.Close()
	}
}

func TestWrongIdentityContactOrRelayAssociationCannotReconnect(t *testing.T) {
	for _, kind := range []string{"server-key", "client-key", "contact-id", "relay-association", "same-identity"} {
		t.Run(kind, func(t *testing.T) {
			a, b := optionsPair(t)
			unknown, _ := identity.Generate()
			switch kind {
			case "server-key":
				b.Peer = unknown.PublicKey()
			case "client-key":
				a.Peer = unknown.PublicKey()
			case "contact-id":
				b.ContactID = base64.RawURLEncoding.EncodeToString([]byte("synthetic-id-002"))
			case "relay-association":
				b.Association[0] ^= 1
			case "same-identity":
				b.Local = a.Local
			}
			left, right := connect(t, a, b)
			if kind == "client-key" {
				// TLS 1.3 lets the client finish its local handshake before the
				// server has accepted its certificate. The decisive boundary is
				// that the verifier rejects it and the rejected client cannot
				// receive application data from an admitted server session.
				if left.session != nil || left.err == nil {
					t.Fatal("server admitted an unapproved client identity")
				}
				if right.session != nil {
					_ = right.session.SetReadDeadline(time.Now().Add(time.Second))
					if _, err := right.session.Read(make([]byte, 1)); err == nil {
						t.Fatal("rejected client received application data")
					}
				}
				return
			}
			if left.session != nil || right.session != nil || left.err == nil || right.err == nil {
				t.Fatal("unapproved identity or association gained a session")
			}
		})
	}
}

func TestDowngradeAndMissingClientCertificateAreRejected(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			a, b := optionsPair(t)
			left, right := net.Pipe()
			defer right.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan result, 1)
			go func() { s, e := Authenticate(ctx, left, a); done <- result{s, e} }()
			config := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12, MaxVersion: tls.VersionTLS12}
			if missing {
				config.MinVersion = tls.VersionTLS13
				config.MaxVersion = tls.VersionTLS13
				config.NextProtos = []string{protocol + "." + b.ContactID + "." + base64.RawURLEncoding.EncodeToString(b.Association[:])}
			}
			_ = right.SetDeadline(time.Now().Add(time.Second))
			client := tls.Client(right, config)
			_ = client.HandshakeContext(ctx)
			got := <-done
			if got.session != nil || got.err == nil {
				t.Fatal("downgrade or missing certificate accepted")
			}
		})
	}
}

func TestPreviousMessagingWireVersionCannotAuthenticate(t *testing.T) {
	a, b := optionsPair(t)
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan result, 1)
	go func() { session, err := Authenticate(ctx, left, a); done <- result{session, err} }()
	certificate, err := newCertificate(b.Local, time.Now(), time.Now().Add(MaxLifetime))
	if err != nil {
		t.Fatal(err)
	}
	legacyALPN := "whatsup-contact/1." + b.ContactID + "." + base64.RawURLEncoding.EncodeToString(b.Association[:])
	client := tls.Client(right, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, NextProtos: []string{legacyALPN}})
	_ = right.SetDeadline(time.Now().Add(time.Second))
	if err := client.HandshakeContext(ctx); err == nil {
		t.Fatal("previous message framing negotiated a new session")
	}
	if got := <-done; got.err == nil || got.session != nil {
		t.Fatal("old framing gained authority to exchange new metadata")
	}
}

func TestPeerCertificateProfileAndResumptionFailClosed(t *testing.T) {
	a, b := optionsPair(t)
	cert, err := newCertificate(b.Local, time.Now(), time.Now().Add(MaxLifetime))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	alpn := protocol + "." + a.ContactID + "." + base64.RawURLEncoding.EncodeToString(a.Association[:])
	base := tls.ConnectionState{Version: tls.VersionTLS13, NegotiatedProtocol: alpn, PeerCertificates: []*x509.Certificate{parsed}}
	if verifyPeer(base, a.Peer, alpn, time.Now()) != nil {
		t.Fatal("valid exact identity profile rejected")
	}
	for _, kind := range []string{"resumed", "expired", "not-yet-valid", "chain", "wrong-usage", "wrong-alpn"} {
		t.Run(kind, func(t *testing.T) {
			state := base
			copyCert := *parsed
			state.PeerCertificates = []*x509.Certificate{&copyCert}
			switch kind {
			case "resumed":
				state.DidResume = true
			case "expired":
				copyCert.NotAfter = time.Now().Add(-time.Second)
			case "not-yet-valid":
				copyCert.NotBefore = time.Now().Add(time.Hour)
			case "chain":
				state.PeerCertificates = append(state.PeerCertificates, parsed)
			case "wrong-usage":
				copyCert.KeyUsage = x509.KeyUsageCertSign
			case "wrong-alpn":
				state.NegotiatedProtocol = "other"
			}
			if verifyPeer(state, a.Peer, alpn, time.Now()) == nil {
				t.Fatal("invalid peer profile accepted")
			}
		})
	}
}

func TestCanceledAuthenticationAndPrivateFormatting(t *testing.T) {
	a, _ := optionsPair(t)
	left, right := net.Pipe()
	defer right.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, e := Authenticate(ctx, left, a); s != nil || e == nil {
		t.Fatal("canceled authentication accepted")
	}
	if _, e := right.Write([]byte{1}); e == nil {
		t.Fatal("failed authentication retained raw transport")
	}
	for _, value := range []any{Session{}, &Session{}} {
		if _, e := json.Marshal(value); e == nil {
			t.Fatal("session serialized")
		}
		if fmt.Sprintf("%#v", value) != "[private authenticated WhatsUp contact session]" {
			t.Fatal("session formatting exposed internal fields")
		}
	}
}
