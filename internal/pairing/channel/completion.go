package channel

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"time"

	"whatsup/internal/identity"
)

// ActivationAssociation binds the public facts of one agreed credential bundle.
// PSKCommitment is SHA-256 of the random 256-bit PSK, not a replacement PSK or KDF.
// It is backend-only; ordinary formatting and JSON cannot expose the association.
type ActivationAssociation struct {
	ID                [16]byte
	InviterIdentity   identity.PublicKey
	JoinerIdentity    identity.PublicKey
	InviterNode       [32]byte
	JoinerNode        [32]byte
	PSKCommitment     [32]byte
	RoutingCommitment [32]byte
}

const associationSize = 1 + 16 + 32*6

var ErrCompletion = errors.New("contact activation completion failed")

func (ActivationAssociation) String() string               { return "[private WhatsUp activation association]" }
func (ActivationAssociation) GoString() string             { return "[private WhatsUp activation association]" }
func (ActivationAssociation) MarshalJSON() ([]byte, error) { return nil, ErrCompletion }

func (a ActivationAssociation) Valid() bool {
	return a.ID != [16]byte{} && a.InviterIdentity.Valid() && a.JoinerIdentity.Valid() && !a.InviterIdentity.Equal(a.JoinerIdentity) && a.InviterNode != [32]byte{} && a.JoinerNode != [32]byte{} && a.InviterNode != a.JoinerNode && a.PSKCommitment != [32]byte{}
}

// Equal compares a fixed, versioned canonical layout without JSON ambiguities.
func (a ActivationAssociation) Equal(other ActivationAssociation) bool {
	x, y := a.encode(), other.encode()
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

func (a ActivationAssociation) encode() [associationSize]byte {
	var wire [associationSize]byte
	wire[0] = 2
	copy(wire[1:17], a.ID[:])
	copy(wire[17:49], a.InviterIdentity[:])
	copy(wire[49:81], a.JoinerIdentity[:])
	copy(wire[81:113], a.InviterNode[:])
	copy(wire[113:145], a.JoinerNode[:])
	copy(wire[145:177], a.PSKCommitment[:])
	copy(wire[177:], a.RoutingCommitment[:])
	return wire
}

func associationForAgreement(state *sessionState, agreement ContactAgreement) ActivationAssociation {
	a := ActivationAssociation{ID: agreement.ID, PSKCommitment: sha256.Sum256(agreement.PresharedKey[:])}
	a.RoutingCommitment, _ = RoutingCommitment(agreement.RelayServiceURL, agreement.ServerEndpoint)
	if state.role == Inviter {
		a.InviterIdentity, a.JoinerIdentity = state.local, state.peer
		a.InviterNode, a.JoinerNode = agreement.LocalNode, agreement.PeerNode
	} else {
		a.InviterIdentity, a.JoinerIdentity = state.peer, state.local
		a.InviterNode, a.JoinerNode = agreement.PeerNode, agreement.LocalNode
	}
	return a
}

// Completion is produced only after local durable commit, a matching peer
// COMMITTED frame and the role-ordered terminal receipt on the reserved TLS
// channel. It is not a restored authority.
// Copies share consumption, expiry and closure. Read-only association getters
// remain facts after closure; only the live guards can authorize a bounded step.
type Completion struct{ state *sessionState }

func (Completion) String() string               { return "[private WhatsUp activation completion]" }
func (Completion) GoString() string             { return "[private WhatsUp activation completion]" }
func (Completion) MarshalJSON() ([]byte, error) { return nil, ErrCompletion }

func (c *Completion) shared() *sessionState {
	if c == nil {
		return nil
	}
	return c.state
}
func (c *Completion) Local() identity.PublicKey { return (&Session{state: c.shared()}).Local() }
func (c *Completion) Peer() identity.PublicKey  { return (&Session{state: c.shared()}).Peer() }
func (c *Completion) Role() Role                { return (&Session{state: c.shared()}).Role() }
func (c *Completion) InvitationID() [16]byte    { return (&Session{state: c.shared()}).InvitationID() }
func (c *Completion) ExpiresAt() time.Time      { return (&Session{state: c.shared()}).ExpiresAt() }

func (c *Completion) Association() ActivationAssociation {
	if c == nil || c.state == nil {
		return ActivationAssociation{}
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	return c.state.activation
}

func (c *Completion) MatchesSession(session *Session) bool {
	return c != nil && c.state != nil && session != nil && c.state == session.state
}

func (c *Completion) MatchesAssociation(association ActivationAssociation) bool {
	return c != nil && c.state != nil && association.Valid() && c.Association().Equal(association)
}

// WithLiveCompletion holds only the channel guard across a bounded non-IO
// operation. The callback must not reenter channel methods or retire listeners.
func (c *Completion) WithLiveCompletion(operation func() error) error {
	return c.withLive(operation, false)
}

// ConsumeLiveOperation permits exactly one successful activation step. Listener
// retirement must occur after returning, outside this guard and manager locks.
func (c *Completion) ConsumeLiveOperation(operation func() error) error {
	return c.withLive(operation, true)
}

func (c *Completion) withLive(operation func() error, consume bool) error {
	if c == nil || c.state == nil || operation == nil {
		return ErrInvalid
	}
	state := c.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.liveLocked(); err != nil {
		return err
	}
	if state.phase != approved || !state.completed || state.completionConsumed {
		return ErrUsed
	}
	if err := operation(); err != nil {
		return err
	}
	if consume {
		state.completionConsumed = true
	}
	return nil
}

// CompleteActivation preserves the invitation channel until both sides have
// committed the exact provisioned association. The joiner's terminal receipt
// proves to the inviter that its COMMITTED frame was consumed before retirement.
// The invitation transport is then retired gracefully, with the joiner
// initiating the authenticated stream end. Its session proof stays logically
// live only for bounded activation until consumption, explicit close or expiry.
// The callbacks perform durable writes outside the channel mutex. Failure may
// leave a committed but inert record; a failed local exchange creates no proof.
func (a *Approval) CompleteActivation(ctx context.Context, reservation *Reservation, association ActivationAssociation, persistCommit func() error) (*Completion, error) {
	if a == nil || a.state == nil || reservation == nil || reservation.state != a.state || ctx == nil || persistCommit == nil || !association.Valid() {
		return nil, ErrInvalid
	}
	state := a.state
	session := &Session{state: state}
	if contextEnded(ctx) {
		_ = session.Close()
		return nil, ErrCanceled
	}
	state.mu.Lock()
	if err := state.liveLocked(); err != nil {
		state.mu.Unlock()
		return nil, err
	}
	if state.phase != approved || !state.reserved || !state.provisioned || state.completionStarted {
		state.mu.Unlock()
		return nil, ErrUsed
	}
	if !state.activation.Equal(association) {
		state.mu.Unlock()
		_ = session.Close()
		return nil, ErrCompletion
	}
	state.completionStarted = true
	binding, role := state.binding, state.role
	state.mu.Unlock()
	defer clear(binding[:])
	wire := association.encode()
	opposite := Inviter
	if role == Inviter {
		opposite = Joiner
	}
	err := session.operation(ctx, func() error {
		// Persist before announcing local COMMITTED. Serialize the frame order
		// by role so even a zero-buffer net.Pipe cannot deadlock two writers.
		if contextEnded(ctx) {
			return ErrCanceled
		}
		state.mu.Lock()
		liveErr := state.liveLocked()
		state.mu.Unlock()
		if liveErr != nil {
			return liveErr
		}
		if persistCommit() != nil {
			return ErrPersistence
		}
		write := func(kind byte) error {
			packet := makePacket("WUCOM001", kind, role, binding, associationSize)
			defer clear(packet)
			copy(packet[frameSize:], wire[:])
			return writePacket(state.tls, packet)
		}
		read := func(kind byte) error {
			packet, err := readPacket(state.tls, "WUCOM001", kind, opposite, binding, associationSize)
			if err != nil {
				return err
			}
			defer clear(packet)
			if subtle.ConstantTimeCompare(packet[frameSize:], wire[:]) != 1 {
				return ErrCompletion
			}
			return nil
		}
		if role == Inviter {
			if err := read(1); err != nil {
				return err
			}
			if err := write(1); err != nil {
				return err
			}
			// A successful TLS Write can mean only that bytes were queued in
			// the tunnel. Do not retire the inviter's listener until the joiner
			// has authenticated and acknowledged this exact final commit.
			if err := read(2); err != nil {
				return err
			}
			return session.retireTransport(ctx)
		}
		if err := write(1); err != nil {
			return err
		}
		if err := read(1); err != nil {
			return err
		}
		if err := write(2); err != nil {
			return err
		}
		return session.retireTransport(ctx)
	})
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.liveLocked(); err != nil {
		return nil, err
	}
	if state.phase != approved || state.completed {
		return nil, ErrUsed
	}
	state.completed = true
	return &Completion{state: state}, nil
}
