// Package sessionauth automatically authenticates a saved contact on every new
// connection using standard mutual TLS 1.3 and its previously approved identity.
// It has no user confirmation, invitation, listener, storage or relay-selection API.
package sessionauth

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"whatsup/internal/identity"
)

const (
	HandshakeTimeout = 15 * time.Second
	MaxLifetime      = 12 * time.Hour
	// Certificates prove possession of pinned keys, rather than website PKI.
	// Permit bounded desktop/VM clock drift without changing the local session
	// lifetime or accepting certificates with unbounded validity intervals.
	MaxClockSkew    = 5 * time.Minute
	certificateName = "WhatsUp contact v1"
	// Version 4 adds bounded attachment offers, approval and chunk checkpoints.
	// Protocol 3 remains supported for text, profile, heartbeat and capacity frames.
	// Wire protocol versions are independent of application release numbers.
	protocol        = "whatsup-contact/4"
	controlProtocol = "whatsup-contact/4-control1"
	// Previous protocol framing is selected only after exact-contact mutual TLS.
	previousProtocol = "whatsup-contact/3"
	// Wire 2 is diagnostic-only: authenticate its exact pin and context before
	// reporting incompatibility, then close without creating a Session.
	diagnosticProtocol = "whatsup-contact/2"
)

var (
	ErrAuthentication  = errors.New("saved contact authentication failed")
	ErrClosed          = errors.New("contact session is closed")
	ErrIdentityChanged = errors.New("saved contact identity could not be verified")
	ErrIncompatible    = errors.New("authenticated contact uses an incompatible messaging protocol")
)

type Options struct {
	Local       *identity.Identity
	Peer        identity.PublicKey
	ContactID   string
	Association [32]byte
	Server      bool
}

// Session is an authenticated stream, never a durable contact or restored proof.
// Copies share closure. Only its guarded methods establish live peer authority.
type Session struct{ state *sessionState }

// Capabilities are fixed by the authenticated TLS transcript. They describe
// framing support, never local approval or permission to perform an action.
type Capabilities struct {
	WireVersion uint8
	Attachments bool
	Control     bool
}

func (s *Session) Capabilities() Capabilities {
	if s == nil || s.state == nil {
		return Capabilities{}
	}
	return s.state.capabilities
}

type sessionState struct {
	mu           sync.Mutex
	raw          net.Conn
	tls          *tls.Conn
	local, peer  identity.PublicKey
	contactID    string
	association  [32]byte
	capabilities Capabilities
	expires      time.Time
	closed       bool
	stop         func() bool
	timer        *time.Timer
}

func (Session) String() string               { return "[private authenticated WhatsUp contact session]" }
func (Session) GoString() string             { return "[private authenticated WhatsUp contact session]" }
func (Session) MarshalJSON() ([]byte, error) { return nil, ErrAuthentication }

func validID(value string) bool {
	if len(value) != 22 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 16 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

// Authenticate takes ownership of raw on every outcome. ALPN binds the contact
// ID and canonical credential/relay-policy association into the TLS transcript.
// VerifyConnection replaces website PKI with mandatory exact Ed25519 key pins.
func Authenticate(ctx context.Context, raw net.Conn, options Options) (*Session, error) {
	return authenticate(ctx, raw, options, [][32]byte{options.Association}, false)
}

// AuthenticateCandidates permits one server-side physical listener to serve at
// most two explicitly approved route associations. The caller must validate each
// candidate against its durable consent ledger before and after authentication.
// Clients still offer exactly one association; the returned proof is bound only
// to the association actually selected in the authenticated TLS transcript.
func AuthenticateCandidates(ctx context.Context, raw net.Conn, options Options, associations [][32]byte) (*Session, error) {
	return authenticate(ctx, raw, options, associations, true)
}

func authenticate(ctx context.Context, raw net.Conn, options Options, associations [][32]byte, candidates bool) (*Session, error) {
	if raw == nil {
		return nil, ErrAuthentication
	}
	ok := false
	defer func() {
		if !ok {
			_ = raw.Close()
		}
	}()
	if ctx == nil || ctx.Err() != nil || options.Local == nil || !options.Peer.Valid() || options.Local.PublicKey().Equal(options.Peer) || !validID(options.ContactID) || options.Association == [32]byte{} {
		return nil, ErrAuthentication
	}
	if candidates && !options.Server || len(associations) < 1 || len(associations) > 2 || associations[0] != options.Association {
		return nil, ErrAuthentication
	}
	associations = append([][32]byte(nil), associations...)
	type negotiatedRoute struct {
		association  [32]byte
		capabilities Capabilities
		diagnostic   bool
	}
	routes := make(map[string]negotiatedRoute, 4*len(associations))
	nextProtos := make([]string, 0, 4*len(associations))
	for i, association := range associations {
		if association == [32]byte{} || i > 0 && association == associations[0] {
			return nil, ErrAuthentication
		}
	}
	// Prefer usable framing across both exact candidates before offering any
	// diagnostic-only ALPN. No bare protocol or wildcard context is advertised.
	for _, choice := range []struct {
		name string
		caps Capabilities
	}{
		{controlProtocol, Capabilities{WireVersion: 4, Attachments: true, Control: true}},
		{protocol, Capabilities{WireVersion: 4, Attachments: true}},
		{previousProtocol, Capabilities{WireVersion: 3}},
		{diagnosticProtocol, Capabilities{}},
	} {
		for _, association := range associations {
			suffix := "." + options.ContactID + "." + base64.RawURLEncoding.EncodeToString(association[:])
			alpn := choice.name + suffix
			nextProtos = append(nextProtos, alpn)
			routes[alpn] = negotiatedRoute{association: association, capabilities: choice.caps, diagnostic: choice.name == diagnosticProtocol}
		}
	}
	now := time.Now()
	expires := now.Add(MaxLifetime)
	certificate, err := newCertificate(options.Local, now, expires)
	if err != nil {
		return nil, ErrAuthentication
	}
	verify := func(state tls.ConnectionState) error {
		if _, approved := routes[state.NegotiatedProtocol]; !approved {
			return ErrAuthentication
		}
		return verifyPeer(state, options.Peer, state.NegotiatedProtocol, time.Now())
	}
	var identityRejected atomic.Bool
	config := &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate}, NextProtos: nextProtos,
		ClientAuth: tls.RequireAnyClientCert, SessionTicketsDisabled: true,
		// A saved contact pins an identity, not a DNS name. No connection is
		// accepted without the exact-key and certificate checks below.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if err := verify(state); err != nil {
				identityRejected.Store(errors.Is(err, ErrIdentityChanged))
				// Do not block writing an alert to an uncooperative peer.
				_ = raw.Close()
				return ErrAuthentication
			}
			return nil
		},
	}
	var connection *tls.Conn
	if options.Server {
		connection = tls.Server(raw, config)
	} else {
		connection = tls.Client(raw, config)
	}
	work, cancel := context.WithTimeout(ctx, HandshakeTimeout)
	defer cancel()
	limit, _ := work.Deadline()
	if raw.SetDeadline(limit) != nil {
		return nil, ErrAuthentication
	}
	stopHandshake := context.AfterFunc(work, func() { _ = raw.Close() })
	err = connection.HandshakeContext(work)
	stopped := stopHandshake()
	if identityRejected.Load() {
		return nil, ErrIdentityChanged
	}
	state := connection.ConnectionState()
	if err != nil || !stopped || work.Err() != nil || !state.HandshakeComplete || verify(state) != nil || raw.SetDeadline(time.Time{}) != nil {
		return nil, ErrAuthentication
	}
	selected := routes[state.NegotiatedProtocol]
	if selected.diagnostic {
		return nil, ErrIncompatible
	}
	s := &Session{state: &sessionState{raw: raw, tls: connection, local: options.Local.PublicKey(), peer: options.Peer, contactID: options.ContactID, association: selected.association, capabilities: selected.capabilities, expires: expires}}
	s.state.mu.Lock()
	s.state.stop = context.AfterFunc(ctx, func() { _ = s.Close() })
	s.state.timer = time.AfterFunc(time.Until(expires), func() { _ = s.Close() })
	s.state.mu.Unlock()
	if ctx.Err() != nil {
		_ = s.Close()
		return nil, ErrAuthentication
	}
	ok = true
	return s, nil
}

func verifyPeer(state tls.ConnectionState, expected identity.PublicKey, alpn string, now time.Time) error {
	if state.Version != tls.VersionTLS13 || state.DidResume || state.NegotiatedProtocol != alpn || len(state.PeerCertificates) != 1 {
		return ErrAuthentication
	}
	c := state.PeerCertificates[0]
	name, _ := asn1.Marshal(pkix.Name{CommonName: certificateName}.ToRDNSequence())
	if len(c.Raw) == 0 || len(c.Raw) > 2048 || c.Version != 3 || c.SignatureAlgorithm != x509.PureEd25519 || c.PublicKeyAlgorithm != x509.Ed25519 || c.SerialNumber == nil || c.SerialNumber.Sign() <= 0 || c.SerialNumber.BitLen() > 128 || !c.BasicConstraintsValid || c.IsCA || c.KeyUsage != x509.KeyUsageDigitalSignature || len(c.ExtKeyUsage) != 2 || c.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || c.ExtKeyUsage[1] != x509.ExtKeyUsageClientAuth || len(c.UnknownExtKeyUsage) != 0 || len(c.UnhandledCriticalExtensions) != 0 || len(c.DNSNames) != 0 || len(c.IPAddresses) != 0 || len(c.EmailAddresses) != 0 || len(c.URIs) != 0 || now.Add(MaxClockSkew).Before(c.NotBefore) || !now.Add(-MaxClockSkew).Before(c.NotAfter) || !c.NotBefore.Before(c.NotAfter) || c.NotAfter.Sub(c.NotBefore) > MaxLifetime+time.Minute || subtle.ConstantTimeCompare(name, c.RawSubject) != 1 || subtle.ConstantTimeCompare(c.RawSubject, c.RawIssuer) != 1 || c.CheckSignature(c.SignatureAlgorithm, c.RawTBSCertificate, c.Signature) != nil {
		return ErrAuthentication
	}
	key, ok := c.PublicKey.(ed25519.PublicKey)
	if !ok || len(key) != len(expected) {
		return ErrAuthentication
	}
	if subtle.ConstantTimeCompare(key, expected[:]) != 1 {
		return ErrIdentityChanged
	}
	return nil
}

type identitySigner struct{ local *identity.Identity }

func (s identitySigner) Public() crypto.PublicKey {
	key := s.local.PublicKey()
	return ed25519.PublicKey(key[:])
}
func (s identitySigner) Sign(_ io.Reader, data []byte, options crypto.SignerOpts) ([]byte, error) {
	if options == nil || options.HashFunc() != crypto.Hash(0) {
		return nil, ErrAuthentication
	}
	return s.local.Sign(data), nil
}

func newCertificate(local *identity.Identity, now, expires time.Time) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: certificateName}, NotBefore: now.Add(-time.Minute), NotAfter: expires,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, BasicConstraintsValid: true}
	signer := identitySigner{local}
	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: signer}, err
}

func (s *Session) Matches(local, peer identity.PublicKey, id string, association [32]byte) bool {
	return s != nil && s.state != nil && s.state.local.Equal(local) && s.state.peer.Equal(peer) && s.state.contactID == id && s.state.association == association
}

// WithLivePeer guards one bounded non-IO operation. It cannot manufacture local
// user approval. The caller must separately validate the saved contact record.
func (s *Session) WithLivePeer(peer identity.PublicKey, operation func() error) error {
	if s == nil || s.state == nil || operation == nil {
		return ErrAuthentication
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.closed || !time.Now().Before(s.state.expires) || !s.state.peer.Equal(peer) {
		return ErrClosed
	}
	return operation()
}

func (s *Session) Read(p []byte) (int, error)         { return s.state.tls.Read(p) }
func (s *Session) Write(p []byte) (int, error)        { return s.state.tls.Write(p) }
func (s *Session) LocalAddr() net.Addr                { return s.state.raw.LocalAddr() }
func (s *Session) RemoteAddr() net.Addr               { return s.state.raw.RemoteAddr() }
func (s *Session) SetDeadline(t time.Time) error      { return s.state.raw.SetDeadline(t) }
func (s *Session) SetReadDeadline(t time.Time) error  { return s.state.raw.SetReadDeadline(t) }
func (s *Session) SetWriteDeadline(t time.Time) error { return s.state.raw.SetWriteDeadline(t) }
func (s *Session) Close() error {
	if s == nil || s.state == nil {
		return nil
	}
	s.state.mu.Lock()
	if s.state.closed {
		s.state.mu.Unlock()
		return nil
	}
	s.state.closed = true
	if s.state.stop != nil {
		s.state.stop()
	}
	if s.state.timer != nil {
		s.state.timer.Stop()
	}
	s.state.mu.Unlock()
	return s.state.raw.Close()
}
