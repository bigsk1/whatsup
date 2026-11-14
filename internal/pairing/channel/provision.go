package channel

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"time"
)

const maxInvitationBytes = 2048

var (
	ErrReservation = errors.New("invitation reservation was not accepted")
	ErrPersistence = errors.New("pending contact could not be saved")
)

// Reservation proves that the inviter accepted the bearer on this exact TLS
// session. It is ephemeral and never grants contact or application authority.
type Reservation struct{ state *sessionState }

func (Reservation) String() string   { return "[private WhatsUp reservation]" }
func (Reservation) GoString() string { return "[private WhatsUp reservation]" }
func (Reservation) MarshalJSON() ([]byte, error) {
	return nil, errors.New("invitation reservation cannot be serialized")
}
func (r *Reservation) MatchesSession(session *Session) bool {
	return r != nil && r.state != nil && session != nil && r.state == session.state
}

// WithLiveApproval checks both opaque receipts together for a bounded, non-IO
// operation. Credential generation and persistence run after releasing this
// guard and are rechecked by the typed exchange before any secret is sent.
func (r *Reservation) WithLiveApproval(approval *Approval, operation func() error) error {
	if r == nil || r.state == nil || approval == nil || approval.state != r.state || operation == nil {
		return ErrInvalid
	}
	state := r.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.liveLocked(); err != nil {
		return err
	}
	if !state.reserved || state.phase != approved || state.provisionStarted {
		return ErrUsed
	}
	return operation()
}

// RequestReservation sends the bounded bearer link and waits for authenticated
// acceptance before the joiner can show a fingerprint. The link is never logged.
func (s *Session) RequestReservation(ctx context.Context, link string) (*Reservation, error) {
	if len(link) == 0 || len(link) > maxInvitationBytes {
		_ = s.Close()
		return nil, ErrReservation
	}
	return s.reserve(ctx, Joiner, link, nil)
}

// AcceptReservation invokes reserve only after TLS authentication and before an
// acceptance is sent. The callback must atomically validate/redeem the bearer.
// No channel mutex is held: retirement hooks may immediately close transport.
func (s *Session) AcceptReservation(ctx context.Context, reserve func(string) error) (*Reservation, error) {
	if reserve == nil {
		_ = s.Close()
		return nil, ErrReservation
	}
	return s.reserve(ctx, Inviter, "", reserve)
}

func (s *Session) reserve(ctx context.Context, role Role, link string, reserve func(string) error) (*Reservation, error) {
	if s == nil || s.state == nil || ctx == nil {
		_ = s.Close()
		return nil, ErrInvalid
	}
	state := s.state
	state.mu.Lock()
	if err := state.liveLocked(); err != nil {
		state.mu.Unlock()
		return nil, err
	}
	if state.role != role || state.phase != pending || state.reservationStarted {
		state.mu.Unlock()
		return nil, ErrUsed
	}
	state.reservationStarted = true
	binding := state.binding
	state.mu.Unlock()
	defer clear(binding[:])
	err := s.operation(ctx, func() error {
		if role == Joiner {
			packet := makePacket("WURES001", 1, role, binding, 2+len(link))
			defer clear(packet)
			binary.BigEndian.PutUint16(packet[frameSize:], uint16(len(link)))
			copy(packet[frameSize+2:], link)
			if err := writePacket(state.tls, packet); err != nil {
				return err
			}
			_, err := readPacket(state.tls, "WURES001", 2, Inviter, binding, 0)
			return err
		}
		packet, err := readPacket(state.tls, "WURES001", 1, Joiner, binding, 2)
		if err != nil {
			return err
		}
		size := int(binary.BigEndian.Uint16(packet[frameSize:]))
		clear(packet)
		if size == 0 || size > maxInvitationBytes {
			return ErrReservation
		}
		bearer := make([]byte, size)
		defer clear(bearer)
		if _, err := io.ReadFull(state.tls, bearer); err != nil {
			return ErrProtocol
		}
		if reserve(string(bearer)) != nil {
			return ErrReservation
		}
		return writePacket(state.tls, makePacket("WURES001", 2, Inviter, binding, 0))
	})
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.liveLocked(); err != nil {
		return nil, err
	}
	if state.phase != pending {
		return nil, ErrUsed
	}
	state.reserved = true
	return &Reservation{state: state}, nil
}

// ContactOffer contains only a proposed public node key for joiners. The inviter
// additionally supplies the sole random contact ID and PSK after mutual consent.
// It is backend-only and is deliberately not JSON serializable.
type ContactOffer struct {
	Node            [32]byte
	ID              [16]byte
	PresharedKey    [32]byte
	RelayServiceURL string
	ServerEndpoint  string
}

// ContactAgreement is temporary backend material for the encrypted pending
// write. It does not authorize a Contact, restore credentials, or open transport.
type ContactAgreement struct {
	LocalNode       [32]byte
	PeerNode        [32]byte
	ID              [16]byte
	PresharedKey    [32]byte
	RelayServiceURL string
	ServerEndpoint  string
}

func (ContactOffer) String() string                   { return "[private WhatsUp contact offer]" }
func (ContactOffer) GoString() string                 { return "[private WhatsUp contact offer]" }
func (ContactOffer) MarshalJSON() ([]byte, error)     { return nil, ErrInvalid }
func (ContactAgreement) String() string               { return "[private WhatsUp contact agreement]" }
func (ContactAgreement) GoString() string             { return "[private WhatsUp contact agreement]" }
func (ContactAgreement) MarshalJSON() ([]byte, error) { return nil, ErrInvalid }

// ExchangePending is a one-shot typed protocol. Both persistence callbacks must
// finish before their prepared ACK is sent; no mutex is held during IO. Returning
// success proves acknowledged pending writes, not active contacts or atomicity.
func (a *Approval) ExchangePending(ctx context.Context, reservation *Reservation, offer ContactOffer, persist func(ContactAgreement) error) (ContactAgreement, error) {
	if a == nil || a.state == nil || reservation == nil || reservation.state != a.state || ctx == nil || persist == nil {
		return ContactAgreement{}, ErrInvalid
	}
	state := a.state
	state.mu.Lock()
	if err := state.liveLocked(); err != nil {
		state.mu.Unlock()
		return ContactAgreement{}, err
	}
	if !state.reserved || state.phase != approved || state.provisionStarted {
		state.mu.Unlock()
		return ContactAgreement{}, ErrUsed
	}
	if offer.Node == [32]byte{} || (state.role == Inviter && (offer.ID == [16]byte{} || offer.PresharedKey == [32]byte{})) || (state.role == Joiner && (offer.ID != [16]byte{} || offer.PresharedKey != [32]byte{})) {
		state.mu.Unlock()
		_ = (&Session{state: state}).Close()
		return ContactAgreement{}, ErrInvalid
	}
	if (offer.RelayServiceURL == "" && offer.ServerEndpoint != "") || (offer.RelayServiceURL != "" && (!validRelaySelection(offer.RelayServiceURL) || (state.role == Joiner && offer.ServerEndpoint != ""))) {
		state.mu.Unlock()
		_ = (&Session{state: state}).Close()
		return ContactAgreement{}, ErrInvalid
	}
	if state.role == Inviter {
		if _, err := RoutingCommitment(offer.RelayServiceURL, offer.ServerEndpoint); err != nil {
			state.mu.Unlock()
			_ = (&Session{state: state}).Close()
			return ContactAgreement{}, ErrInvalid
		}
	}
	state.provisionStarted = true
	binding := state.binding
	state.mu.Unlock()
	defer clear(binding[:])
	defer clear(offer.PresharedKey[:])
	result := ContactAgreement{LocalNode: offer.Node, ID: offer.ID, PresharedKey: offer.PresharedKey, RelayServiceURL: offer.RelayServiceURL, ServerEndpoint: offer.ServerEndpoint}
	defer clear(result.PresharedKey[:])
	session := &Session{state: state}
	err := session.operation(ctx, func() error {
		if state.role == Joiner {
			if err := writeProvision(state.tls, 1, Joiner, binding, offer.Node[:], offer.RelayServiceURL, ""); err != nil {
				return err
			}
			proposal, endpoint, err := readProvision(state.tls, 2, Inviter, binding, 112, offer.RelayServiceURL, true)
			if err != nil {
				return err
			}
			defer clear(proposal)
			result.ServerEndpoint = endpoint
			copy(result.ID[:], proposal[frameSize:frameSize+16])
			copy(result.PeerNode[:], proposal[frameSize+16:frameSize+48])
			if subtle.ConstantTimeCompare(proposal[frameSize+48:frameSize+80], offer.Node[:]) != 1 {
				return ErrProtocol
			}
			copy(result.PresharedKey[:], proposal[frameSize+80:])
			if !validAgreement(result) {
				return ErrProtocol
			}
			if persist(result) != nil {
				return ErrPersistence
			}
			if err := writePrepared(state.tls, Joiner, binding, result.ID); err != nil {
				return err
			}
			return readPrepared(state.tls, Inviter, binding, result.ID)
		}
		hello, _, err := readProvision(state.tls, 1, Joiner, binding, 32, offer.RelayServiceURL, false)
		if err != nil {
			return err
		}
		copy(result.PeerNode[:], hello[frameSize:])
		clear(hello)
		if !validAgreement(result) {
			return ErrProtocol
		}
		if persist(result) != nil {
			return ErrPersistence
		}
		proposal := make([]byte, 112)
		defer clear(proposal)
		copy(proposal[:16], result.ID[:])
		copy(proposal[16:48], result.LocalNode[:])
		copy(proposal[48:80], result.PeerNode[:])
		copy(proposal[80:], result.PresharedKey[:])
		if err := writeProvision(state.tls, 2, Inviter, binding, proposal, result.RelayServiceURL, result.ServerEndpoint); err != nil {
			return err
		}
		if err := readPrepared(state.tls, Joiner, binding, result.ID); err != nil {
			return err
		}
		return writePrepared(state.tls, Inviter, binding, result.ID)
	})
	if err != nil {
		return ContactAgreement{}, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.liveLocked(); err != nil {
		return ContactAgreement{}, err
	}
	if state.phase != approved {
		return ContactAgreement{}, ErrClosed
	}
	state.provisioned = true
	state.activation = associationForAgreement(state, result)
	return result, nil
}

func validAgreement(a ContactAgreement) bool {
	return a.ID != [16]byte{} && a.LocalNode != [32]byte{} && a.PeerNode != [32]byte{} && a.LocalNode != a.PeerNode && a.PresharedKey != [32]byte{}
}

func (s *Session) operation(ctx context.Context, operation func() error) error {
	state := s.state
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	deadline := state.expires
	if requested, ok := ctx.Deadline(); ok {
		deadline = earlier(deadline, requested)
	}
	err := state.raw.SetDeadline(deadline)
	if err == nil {
		err = operation()
	}
	stopped := stop()
	if contextEnded(ctx) || !stopped {
		_ = s.Close()
		return ErrCanceled
	}
	if err != nil {
		_ = s.Close()
		if errors.Is(err, ErrReservation) || errors.Is(err, ErrPersistence) || errors.Is(err, ErrDenied) || errors.Is(err, ErrFingerprint) || errors.Is(err, ErrCanceled) {
			return err
		}
		if !time.Now().Before(state.expires) {
			return ErrExpired
		}
		return ErrProtocol
	}
	state.mu.Lock()
	liveErr := state.liveLocked()
	state.mu.Unlock()
	if liveErr != nil {
		_ = s.Close()
		return liveErr
	}
	return nil
}

func makePacket(magic string, kind byte, role Role, binding [32]byte, payload int) []byte {
	packet := make([]byte, frameSize+payload)
	copy(packet[:8], magic)
	packet[8], packet[9] = kind, byte(role)
	copy(packet[10:frameSize], binding[:])
	return packet
}

func writePacket(writer io.Writer, packet []byte) error {
	written, err := writer.Write(packet)
	if err != nil || written != len(packet) {
		return ErrProtocol
	}
	return nil
}

func readPacket(reader io.Reader, magic string, kind byte, role Role, binding [32]byte, payload int) ([]byte, error) {
	packet := make([]byte, frameSize+payload)
	if _, err := io.ReadFull(reader, packet); err != nil {
		clear(packet)
		return nil, ErrProtocol
	}
	if subtle.ConstantTimeCompare(packet[:8], []byte(magic)) != 1 || packet[8] != kind || packet[9] != byte(role) || subtle.ConstantTimeCompare(packet[10:frameSize], binding[:]) != 1 {
		clear(packet)
		return nil, ErrProtocol
	}
	return packet, nil
}

func writePrepared(writer io.Writer, role Role, binding [32]byte, id [16]byte) error {
	packet := makePacket("WUPRE001", 3, role, binding, 16)
	copy(packet[frameSize:], id[:])
	return writePacket(writer, packet)
}
func readPrepared(reader io.Reader, role Role, binding [32]byte, id [16]byte) error {
	packet, err := readPacket(reader, "WUPRE001", 3, role, binding, 16)
	if err != nil {
		return err
	}
	defer clear(packet)
	if subtle.ConstantTimeCompare(packet[frameSize:], id[:]) != 1 {
		return ErrProtocol
	}
	return nil
}
