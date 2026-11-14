// Package channel authenticates the two identity keys of a pending pairing over
// standard TLS 1.3. It owns a caller-supplied connection; it never listens, dials,
// starts Tailcat, persists credentials or exposes chat/history operations.
package channel

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
	"time"

	"whatsup/internal/identity"
)

const (
	MaxLifetime        = 10 * time.Minute
	MaxHandshakeTime   = 15 * time.Second
	MaxClockSkew       = 5 * time.Minute
	maxCertificateSize = 2048
	protocol           = "whatsup-pairing/1"
	certificateName    = "WhatsUp pairing v1"
	frameSize          = 42
)

var (
	ErrInvalid     = errors.New("pairing channel configuration is invalid")
	ErrHandshake   = errors.New("pairing channel authentication failed")
	ErrExpired     = errors.New("pairing channel expired")
	ErrCanceled    = errors.New("pairing channel operation canceled")
	ErrClosed      = errors.New("pairing channel closed")
	ErrFingerprint = errors.New("pairing channel fingerprint does not match")
	ErrUsed        = errors.New("pairing channel approval already attempted")
	ErrDenied      = errors.New("pairing channel approval denied")
	ErrProtocol    = errors.New("pairing channel approval exchange failed")
)

// Role describes this endpoint. It grants no contact or transport authority.
type Role uint8

const (
	Inviter Role = iota + 1 // TLS server, whose identity is pinned by the invitation.
	Joiner                  // TLS client, authenticated before entering pending approval.
)

type phase uint8

const (
	pending phase = iota + 1
	exchanging
	approved
	closed
)

// Session deliberately keeps its TLS connection and exporter out of public DTOs.
// Value copies share state, closure and the one-shot approval exchange.
type Session struct{ state *sessionState }

type sessionState struct {
	mu                 sync.Mutex
	raw                net.Conn
	tls                *tls.Conn
	tlsInput           *eofTrackingConn
	transportRetired   bool
	local              identity.PublicKey
	peer               identity.PublicKey
	role               Role
	invite             [16]byte
	expires            time.Time
	localDeadline      time.Time
	binding            [32]byte
	phase              phase
	timer              *time.Timer
	reservationStarted bool
	reserved           bool
	provisionStarted   bool
	provisioned        bool
	activation         ActivationAssociation
	completionStarted  bool
	completed          bool
	completionConsumed bool
}

// Approval proves only live reciprocal consent on one exact authenticated TLS
// session. It is neither durable activation nor permission to start networking.
// Close or expiry invalidates every copy. There is no serialization/export API.
type Approval struct{ state *sessionState }

func (Session) String() string   { return "[private WhatsUp pairing channel]" }
func (Session) GoString() string { return "[private WhatsUp pairing channel]" }
func (Session) MarshalJSON() ([]byte, error) {
	return nil, errors.New("pairing channel cannot be serialized")
}
func (Approval) String() string   { return "[private WhatsUp pairing approval]" }
func (Approval) GoString() string { return "[private WhatsUp pairing approval]" }
func (Approval) MarshalJSON() ([]byte, error) {
	return nil, errors.New("pairing approval cannot be serialized")
}

func (s *Session) Local() identity.PublicKey {
	if s == nil || s.state == nil {
		return identity.PublicKey{}
	}
	return s.state.local
}
func (s *Session) Peer() identity.PublicKey {
	if s == nil || s.state == nil {
		return identity.PublicKey{}
	}
	return s.state.peer
}
func (s *Session) Role() Role {
	if s == nil || s.state == nil {
		return 0
	}
	return s.state.role
}
func (s *Session) ExpiresAt() time.Time {
	if s == nil || s.state == nil {
		return time.Time{}
	}
	return s.state.expires
}
func (s *Session) InvitationID() [16]byte {
	if s == nil || s.state == nil {
		return [16]byte{}
	}
	return s.state.invite
}

func (a *Approval) Local() identity.PublicKey { return (&Session{state: a.shared()}).Local() }
func (a *Approval) Peer() identity.PublicKey  { return (&Session{state: a.shared()}).Peer() }
func (a *Approval) Role() Role                { return (&Session{state: a.shared()}).Role() }
func (a *Approval) ExpiresAt() time.Time      { return (&Session{state: a.shared()}).ExpiresAt() }
func (a *Approval) InvitationID() [16]byte    { return (&Session{state: a.shared()}).InvitationID() }
func (a *Approval) shared() *sessionState {
	if a == nil {
		return nil
	}
	return a.state
}

func (a *Approval) MatchesSession(session *Session) bool {
	return a != nil && a.state != nil && session != nil && a.state == session.state
}

// WithPendingPeer holds the channel guard across one bounded, non-IO reservation
// operation. Acquire this guard before another component's lock. The callback
// must not call channel methods or perform network IO.
func (s *Session) WithPendingPeer(operation func() error) error {
	if s == nil || s.state == nil || operation == nil {
		return ErrInvalid
	}
	state := s.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.liveLocked(); err != nil {
		return err
	}
	if state.phase != pending {
		return ErrUsed
	}
	return operation()
}

// WithLiveApproval protects one bounded backend operation from concurrent Close.
// It grants no network or durable contact authority. Acquire this guard before
// another component's lock; the callback must not call channel methods or do IO.
func (a *Approval) WithLiveApproval(operation func() error) error {
	if a == nil || a.state == nil || operation == nil {
		return ErrInvalid
	}
	state := a.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.liveLocked(); err != nil {
		return err
	}
	if state.phase != approved {
		return ErrUsed
	}
	return operation()
}

func (s *sessionState) liveLocked() error {
	if !time.Now().Before(s.operationDeadline()) {
		return ErrExpired
	}
	if s.phase == closed {
		return ErrClosed
	}
	return nil
}

// The inviter owns invitation expiry. A joiner's clock cannot decide whether
// its invitation is still valid; its local timeout only bounds resource use.
// Keep expires unchanged for exact invitation/reservation association checks.
func (s *sessionState) operationDeadline() time.Time {
	if s.role == Joiner {
		return s.localDeadline
	}
	return s.expires
}

// NewClient takes ownership of raw, including on failure, and pins the inviter's
// complete identity from the invitation. The caller must authenticate the invite
// bearer and atomically reserve it separately; a TLS connection is not approval.
func NewClient(ctx context.Context, raw net.Conn, local *identity.Identity, expectedOwner identity.PublicKey, invitationID [16]byte, expiresAt time.Time) (*Session, error) {
	return newSession(ctx, raw, local, expectedOwner, invitationID, expiresAt, Joiner)
}

// NewServer requires an Ed25519 client certificate and TLS proof of its private
// key. That authenticated candidate remains pending and has no application API.
func NewServer(ctx context.Context, raw net.Conn, local *identity.Identity, invitationID [16]byte, expiresAt time.Time) (*Session, error) {
	return newSession(ctx, raw, local, identity.PublicKey{}, invitationID, expiresAt, Inviter)
}

func newSession(ctx context.Context, raw net.Conn, local *identity.Identity, expected identity.PublicKey, invitationID [16]byte, expiresAt time.Time, role Role) (*Session, error) {
	if raw == nil {
		return nil, ErrInvalid
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = raw.Close()
		}
	}()
	now := time.Now()
	if ctx == nil || local == nil || !local.PublicKey().Valid() || invitationID == [16]byte{} || (role == Joiner && (!expected.Valid() || expected.Equal(local.PublicKey()))) || expiresAt.IsZero() || (role == Inviter && expiresAt.After(now.Add(MaxLifetime))) {
		return nil, ErrInvalid
	}
	if role == Inviter && !now.Before(expiresAt) {
		return nil, ErrExpired
	}
	if contextEnded(ctx) {
		return nil, ErrCanceled
	}
	localDeadline := expiresAt
	if role == Joiner {
		localDeadline = now.Add(MaxLifetime)
	}
	certificate, err := newCertificate(local, now, localDeadline)
	if err != nil {
		return nil, ErrHandshake
	}
	state := &sessionState{raw: raw, local: local.PublicKey(), role: role, invite: invitationID, expires: expiresAt, localDeadline: localDeadline, phase: pending}
	config := tlsConfig(certificate, state.local, expected, invitationID)
	verify := config.VerifyConnection
	config.VerifyConnection = func(connection tls.ConnectionState) error {
		err := verify(connection)
		if err != nil {
			// Do not wait for an alert write to an uncooperative or unbuffered
			// peer after authentication has already failed.
			_ = raw.Close()
		}
		return err
	}
	state.tlsInput = &eofTrackingConn{Conn: raw}
	if role == Joiner {
		state.tls = tls.Client(state.tlsInput, config)
	} else {
		state.tls = tls.Server(state.tlsInput, config)
	}
	deadline := earlier(localDeadline, now.Add(MaxHandshakeTime))
	if userDeadline, ok := ctx.Deadline(); ok {
		deadline = earlier(deadline, userDeadline)
	}
	if raw.SetDeadline(deadline) != nil {
		return nil, ErrHandshake
	}
	if err := state.tls.HandshakeContext(ctx); err != nil {
		if contextEnded(ctx) {
			return nil, ErrCanceled
		}
		if !time.Now().Before(localDeadline) {
			return nil, ErrExpired
		}
		return nil, ErrHandshake
	}
	connection := state.tls.ConnectionState()
	state.peer, err = verifyPeer(connection, state.local, expected, invitationID, time.Now())
	if err != nil || !connection.HandshakeComplete || connection.DidResume {
		return nil, ErrHandshake
	}
	// RFC 9266 tls-exporter channel binding. It prevents an approval frame from
	// another authenticated session from becoming consent on this one.
	binding, err := connection.ExportKeyingMaterial("EXPORTER-Channel-Binding", nil, len(state.binding))
	if err != nil {
		return nil, ErrHandshake
	}
	copy(state.binding[:], binding)
	clear(binding)
	if contextEnded(ctx) {
		clear(state.binding[:])
		return nil, ErrCanceled
	}
	if !time.Now().Before(localDeadline) {
		clear(state.binding[:])
		return nil, ErrExpired
	}
	if raw.SetDeadline(localDeadline) != nil {
		clear(state.binding[:])
		return nil, ErrHandshake
	}
	session := &Session{state: state}
	state.mu.Lock()
	state.timer = time.AfterFunc(time.Until(localDeadline), func() { _ = session.Close() })
	state.mu.Unlock()
	succeeded = true
	return session, nil
}

func earlier(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func contextEnded(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	deadline, ok := ctx.Deadline()
	return ok && !time.Now().Before(deadline)
}

func tlsConfig(certificate tls.Certificate, local, expected identity.PublicKey, invitationID [16]byte) *tls.Config {
	// The canonical invitation ID is public context, not the bearer secret. ALPN
	// binds it into the TLS handshake and rejects reuse for another invitation.
	alpn := protocol + "." + base64.RawURLEncoding.EncodeToString(invitationID[:])
	return &tls.Config{
		MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate}, NextProtos: []string{alpn},
		ClientAuth: tls.RequireAnyClientCert, SessionTicketsDisabled: true,
		// The invitation pins an identity key, not a CA or DNS hostname. This
		// replaces PKI/hostname verification with mandatory exact-key/profile
		// verification below. It never permits an unverified peer connection.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			_, err := verifyPeer(state, local, expected, invitationID, time.Now())
			return err
		},
	}
}

func verifyPeer(state tls.ConnectionState, local, expected identity.PublicKey, invitationID [16]byte, now time.Time) (identity.PublicKey, error) {
	var peer identity.PublicKey
	alpn := protocol + "." + base64.RawURLEncoding.EncodeToString(invitationID[:])
	if state.Version != tls.VersionTLS13 || state.DidResume || state.NegotiatedProtocol != alpn || len(state.PeerCertificates) != 1 {
		return peer, ErrHandshake
	}
	certificate := state.PeerCertificates[0]
	name, _ := asn1.Marshal(pkix.Name{CommonName: certificateName}.ToRDNSequence())
	if len(certificate.Raw) == 0 || len(certificate.Raw) > maxCertificateSize || certificate.Version != 3 || certificate.SignatureAlgorithm != x509.PureEd25519 || certificate.PublicKeyAlgorithm != x509.Ed25519 || certificate.SerialNumber == nil || certificate.SerialNumber.Sign() <= 0 || certificate.SerialNumber.BitLen() > 128 || !certificate.BasicConstraintsValid || certificate.IsCA || certificate.KeyUsage != x509.KeyUsageDigitalSignature || len(certificate.ExtKeyUsage) != 2 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth || certificate.ExtKeyUsage[1] != x509.ExtKeyUsageClientAuth || len(certificate.UnknownExtKeyUsage) != 0 || len(certificate.UnhandledCriticalExtensions) != 0 || len(certificate.DNSNames) != 0 || len(certificate.IPAddresses) != 0 || len(certificate.EmailAddresses) != 0 || len(certificate.URIs) != 0 || now.Add(MaxClockSkew).Before(certificate.NotBefore) || !now.Add(-MaxClockSkew).Before(certificate.NotAfter) || !certificate.NotBefore.Before(certificate.NotAfter) || certificate.NotAfter.Sub(certificate.NotBefore) > MaxLifetime+time.Minute || subtle.ConstantTimeCompare(name, certificate.RawSubject) != 1 || subtle.ConstantTimeCompare(certificate.RawSubject, certificate.RawIssuer) != 1 || certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature) != nil {
		return peer, ErrHandshake
	}
	key, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || len(key) != len(peer) {
		return peer, ErrHandshake
	}
	copy(peer[:], key)
	if !peer.Valid() || peer.Equal(local) || (expected.Valid() && !peer.Equal(expected)) {
		return identity.PublicKey{}, ErrHandshake
	}
	return peer, nil
}

// identitySigner lets x509/TLS use the existing identity without exporting its
// seed/private key. Only standard pure Ed25519 signing is supported.
type identitySigner struct{ local *identity.Identity }

func (s identitySigner) Public() crypto.PublicKey {
	public := s.local.PublicKey()
	return ed25519.PublicKey(public[:])
}
func (s identitySigner) Sign(_ io.Reader, message []byte, options crypto.SignerOpts) ([]byte, error) {
	if options == nil || options.HashFunc() != crypto.Hash(0) {
		return nil, ErrInvalid
	}
	return s.local.Sign(message), nil
}

func newCertificate(local *identity.Identity, now, expires time.Time) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: certificateName},
		NotBefore: now.Add(-time.Minute), NotAfter: expires,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	signer := identitySigner{local: local}
	der, err := x509.CreateCertificate(rand.Reader, template, template, signer.Public(), signer)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: signer}, nil
}

// Approve compares the complete peer fingerprint and performs one reciprocal
// consent exchange. Both peers call concurrently with their own trusted-channel
// confirmation. Failure burns the channel; no Contact/activation is produced.
func (s *Session) Approve(ctx context.Context, fingerprint string) (*Approval, error) {
	if s == nil || s.state == nil {
		return nil, ErrInvalid
	}
	expected := s.Peer().Fingerprint()
	if len(fingerprint) != len(expected) || subtle.ConstantTimeCompare([]byte(fingerprint), []byte(expected)) != 1 {
		_ = s.Close()
		return nil, ErrFingerprint
	}
	if err := s.decide(ctx, true); err != nil {
		return nil, err
	}
	return &Approval{state: s.state}, nil
}

// Deny performs the same bounded one-shot exchange with an explicit refusal.
// Its peer receives ErrDenied. No approval capability can result on either side.
func (s *Session) Deny(ctx context.Context) error {
	err := s.decide(ctx, false)
	if errors.Is(err, ErrDenied) {
		return nil
	}
	return err
}

func (s *Session) decide(ctx context.Context, consent bool) error {
	if s == nil || s.state == nil {
		return ErrInvalid
	}
	if ctx == nil {
		_ = s.Close()
		return ErrInvalid
	}
	state := s.state
	state.mu.Lock()
	if err := state.liveLocked(); err != nil {
		state.mu.Unlock()
		return err
	}
	if state.phase != pending {
		state.mu.Unlock()
		return ErrUsed
	}
	if state.reservationStarted && !state.reserved {
		state.mu.Unlock()
		return ErrUsed
	}
	state.phase = exchanging
	binding := state.binding
	state.mu.Unlock()
	defer clear(binding[:])
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	deadline := state.operationDeadline()
	if requested, ok := ctx.Deadline(); ok {
		deadline = earlier(deadline, requested)
	}
	err := state.raw.SetDeadline(deadline)
	if err == nil {
		err = exchangeDecision(state.tls, state.role, consent, binding)
	}
	stopped := stop()
	if contextEnded(ctx) || !stopped {
		_ = s.Close()
		return ErrCanceled
	}
	state.mu.Lock()
	if liveErr := state.liveLocked(); liveErr != nil {
		state.mu.Unlock()
		_ = s.Close()
		return liveErr
	}
	if err == nil && state.phase == exchanging {
		state.phase = approved
		state.mu.Unlock()
		return nil
	}
	state.mu.Unlock()
	_ = s.Close()
	if errors.Is(err, ErrDenied) {
		return ErrDenied
	}
	return ErrProtocol
}

func exchangeDecision(connection io.ReadWriter, role Role, consent bool, binding [32]byte) error {
	var outgoing, incoming [frameSize]byte
	defer clear(outgoing[:])
	defer clear(incoming[:])
	copy(outgoing[:8], "WUPAIR01")
	if consent {
		outgoing[8] = 1
	} else {
		outgoing[8] = 2
	}
	outgoing[9] = byte(role)
	copy(outgoing[10:], binding[:])
	read := func() error {
		_, err := io.ReadFull(connection, incoming[:])
		return err
	}
	write := func() error {
		written, err := connection.Write(outgoing[:])
		if err == nil && written != len(outgoing) {
			return io.ErrShortWrite
		}
		return err
	}
	var err error
	if role == Joiner {
		err = write()
		if err == nil {
			err = read()
		}
	} else {
		err = read()
		if err == nil {
			err = validateDecision(incoming, outgoing, binding, role)
			if errors.Is(err, ErrDenied) {
				outgoing[8] = 2
				err = nil
			}
			if err == nil {
				err = write()
			}
		}
	}
	if err != nil {
		return ErrProtocol
	}
	if err := validateDecision(incoming, outgoing, binding, role); err != nil {
		return err
	}
	if !consent {
		return ErrDenied
	}
	return nil
}

func validateDecision(incoming, outgoing [frameSize]byte, binding [32]byte, role Role) error {
	if subtle.ConstantTimeCompare(incoming[:8], outgoing[:8]) != 1 || incoming[9] == byte(role) || (incoming[9] != byte(Inviter) && incoming[9] != byte(Joiner)) || subtle.ConstantTimeCompare(incoming[10:], binding[:]) != 1 || (incoming[8] != 1 && incoming[8] != 2) {
		return ErrProtocol
	}
	if incoming[8] == 2 {
		return ErrDenied
	}
	return nil
}

// Close immediately closes the underlying transport rather than blocking on a
// TLS close-notify write to a peer that is no longer reading.
func (s *Session) Close() error {
	if s == nil || s.state == nil {
		return nil
	}
	state := s.state
	state.mu.Lock()
	if state.phase == closed {
		state.mu.Unlock()
		return nil
	}
	state.phase = closed
	clear(state.binding[:])
	if state.timer != nil {
		state.timer.Stop()
	}
	state.mu.Unlock()
	if state.raw.Close() != nil {
		return ErrClosed
	}
	return nil
}
