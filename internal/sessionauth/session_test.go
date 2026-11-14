package sessionauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
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
		if left.session.Capabilities() != (Capabilities{WireVersion: 4, Attachments: true, Control: true}) || right.session.Capabilities() != left.session.Capabilities() {
			t.Fatal("current peers did not negotiate authenticated extended capabilities")
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

func TestUnsupportedHistoricalWireVersionCannotAuthenticate(t *testing.T) {
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

type corruptHandshakeSigner struct{ crypto.Signer }

func (s corruptHandshakeSigner) Sign(random io.Reader, digest []byte, options crypto.SignerOpts) ([]byte, error) {
	signature, err := s.Signer.Sign(random, digest, options)
	if err == nil && len(signature) != 0 {
		signature[0] ^= 1
	}
	return signature, err
}

func TestPreviousProtocolGrantsOnlyNegotiatedCapabilitiesAfterPinnedTLSCompletes(t *testing.T) {
	for _, newServer := range []bool{true, false} {
		for _, kind := range []string{"valid", "wrong-key", "wrong-association", "future-certificate", "expired-certificate", "invalid-profile", "bad-handshake-signature", "prefer-current", "prefer-extended", "unsupported"} {
			t.Run(fmt.Sprintf("server=%v/%s", newServer, kind), func(t *testing.T) {
				current, previous := optionsPair(t)
				current.Server = newServer
				if kind == "wrong-key" {
					previous.Local, _ = identity.Generate()
				}
				if kind == "wrong-association" {
					previous.Association[0] ^= 1
				}
				now := time.Now()
				if kind == "future-certificate" {
					now = now.Add(MaxClockSkew + 2*time.Minute)
				}
				if kind == "expired-certificate" {
					now = now.Add(-MaxLifetime - MaxClockSkew - 2*time.Minute)
				}
				certificate, err := newCertificate(previous.Local, now, now.Add(MaxLifetime))
				if err != nil {
					t.Fatal(err)
				}
				if kind == "invalid-profile" {
					template, err := x509.ParseCertificate(certificate.Certificate[0])
					if err != nil {
						t.Fatal(err)
					}
					template.RawSubject = nil
					template.Subject.CommonName = "unsupported certificate profile"
					signer := identitySigner{previous.Local}
					der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
					if err != nil {
						t.Fatal(err)
					}
					certificate.Certificate[0] = der
				}
				if kind == "bad-handshake-signature" {
					certificate.PrivateKey = corruptHandshakeSigner{certificate.PrivateKey.(crypto.Signer)}
				}
				association := "." + previous.ContactID + "." + base64.RawURLEncoding.EncodeToString(previous.Association[:])
				protos := []string{previousProtocol + association}
				if kind == "prefer-current" {
					// Base /4 stays interoperable without claiming new controls.
					protos = []string{protocol + association, previousProtocol + association}
					if newServer {
						protos[0], protos[1] = protos[1], protos[0]
					}
				}
				if kind == "prefer-extended" {
					protos = []string{controlProtocol + association, protocol + association, previousProtocol + association}
					if newServer {
						protos[0], protos[2] = protos[2], protos[0]
					}
				}
				if kind == "unsupported" {
					protos = []string{"whatsup-contact/99" + association}
				}
				legacyConfig := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
					Certificates: []tls.Certificate{certificate}, NextProtos: protos,
					ClientAuth: tls.RequireAnyClientCert, SessionTicketsDisabled: true,
					VerifyConnection: func(state tls.ConnectionState) error {
						return verifyPeer(state, previous.Peer, state.NegotiatedProtocol, time.Now())
					}}
				left, right := net.Pipe()
				t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				done := make(chan result, 1)
				go func() { session, err := Authenticate(ctx, left, current); done <- result{session, err} }()
				var legacy *tls.Conn
				if newServer {
					legacy = tls.Client(right, legacyConfig)
				} else {
					legacy = tls.Server(right, legacyConfig)
				}
				handshakeErr := legacy.HandshakeContext(ctx)
				got := <-done
				if kind == "prefer-current" || kind == "prefer-extended" {
					selected, caps := protocol, Capabilities{WireVersion: 4, Attachments: true}
					if kind == "prefer-extended" {
						selected = controlProtocol
						caps.Control = true
					}
					if got.session == nil || got.err != nil || handshakeErr != nil || legacy.ConnectionState().NegotiatedProtocol != selected+association || got.session.Capabilities() != caps {
						t.Fatalf("current protocol was not selected when both support it: %v / %v", got.err, handshakeErr)
					}
					_ = got.session.Close()
					return
				}
				if kind == "valid" {
					if got.session == nil || got.err != nil || handshakeErr != nil || got.session.Capabilities() != (Capabilities{WireVersion: 3}) {
						t.Fatal("previous protocol did not establish exact messaging-only authority")
					}
					_ = got.session.Close()
					return
				}
				if got.session != nil {
					t.Fatal("legacy or unauthenticated peer obtained application authority")
				}
				want := ErrAuthentication
				if kind == "wrong-key" {
					want = ErrIdentityChanged
				}
				if !errors.Is(got.err, want) {
					t.Fatalf("wrong diagnostic/authentication classification: got %v, want %v", got.err, want)
				}
				_ = legacy.SetReadDeadline(time.Now().Add(time.Second))
				var application [1]byte
				if n, err := legacy.Read(application[:]); n != 0 || err == nil {
					t.Fatal("diagnostic/rejected peer received application bytes")
				}
			})
		}
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
				copyCert.NotAfter = time.Now().Add(-MaxClockSkew - time.Second)
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

func TestCertificateClockSkewDoesNotChangeIdentityClassification(t *testing.T) {
	a, b := optionsPair(t)
	alpn := protocol + "." + a.ContactID + "." + base64.RawURLEncoding.EncodeToString(a.Association[:])
	now := time.Now()
	for _, skew := range []time.Duration{-4 * time.Minute, 0, 4 * time.Minute} {
		certificate, err := newCertificate(b.Local, now.Add(skew), now.Add(skew).Add(MaxLifetime))
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		state := tls.ConnectionState{Version: tls.VersionTLS13, NegotiatedProtocol: alpn, PeerCertificates: []*x509.Certificate{parsed}}
		if err := verifyPeer(state, a.Peer, alpn, now); err != nil {
			t.Fatalf("ordinary clock skew rejected: %v", err)
		}
		wrong, _ := identity.Generate()
		if err := verifyPeer(state, wrong.PublicKey(), alpn, now); !errors.Is(err, ErrIdentityChanged) {
			t.Fatalf("actual key mismatch did not stop reconnect: %v", err)
		}
		state.NegotiatedProtocol = "invalid"
		if err := verifyPeer(state, a.Peer, alpn, now); !errors.Is(err, ErrAuthentication) || errors.Is(err, ErrIdentityChanged) {
			t.Fatalf("profile failure misreported as identity change: %v", err)
		}
	}
}

func TestRealHandshakeDistinguishesChangedIdentityFromInvalidCertificate(t *testing.T) {
	for _, kind := range []string{"clock-ahead", "clock-behind", "outside-skew", "wrong-key"} {
		t.Run(kind, func(t *testing.T) {
			a, b := optionsPair(t)
			now := time.Now()
			shift := 4 * time.Minute
			if kind == "clock-behind" {
				shift = -4 * time.Minute
			}
			if kind == "outside-skew" {
				shift = MaxClockSkew + 2*time.Minute
			}
			if kind == "wrong-key" {
				b.Local, _ = identity.Generate()
			}
			certificate, err := newCertificate(b.Local, now.Add(shift), now.Add(shift).Add(MaxLifetime))
			if err != nil {
				t.Fatal(err)
			}
			left, right := net.Pipe()
			t.Cleanup(func() { _ = left.Close(); _ = right.Close() })
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan result, 1)
			go func() { s, e := Authenticate(ctx, left, a); done <- result{s, e} }()
			alpn := protocol + "." + a.ContactID + "." + base64.RawURLEncoding.EncodeToString(a.Association[:])
			client := tls.Client(right, &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, NextProtos: []string{alpn}})
			_ = client.HandshakeContext(ctx)
			got := <-done
			switch kind {
			case "wrong-key":
				if got.session != nil || !errors.Is(got.err, ErrIdentityChanged) {
					t.Fatalf("actual key mismatch not classified: %v", got.err)
				}
			case "outside-skew":
				if got.session != nil || !errors.Is(got.err, ErrAuthentication) || errors.Is(got.err, ErrIdentityChanged) {
					t.Fatalf("certificate date error falsely changed identity: %v", got.err)
				}
			default:
				if got.session == nil || got.err != nil {
					t.Fatalf("bounded clock skew broke pinned authentication: %v", got.err)
				}
				_ = got.session.Close()
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
