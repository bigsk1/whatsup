package pairing

import (
	"errors"
	"sync"

	"whatsup/internal/identity"
	"whatsup/internal/pairing/channel"
)

var ErrPeer = errors.New("authenticated peer is not the approved contact")

// Contact binds one local identity to exactly one approved peer. This is a local
// authorization record, not evidence of completed mutual pairing or a live link.
// All fields are private so ordinary JSON serialization cannot expose keys.
type Contact struct{ state *contactState }

// Copies of a Contact must share approval, provisioning and revocation. Keeping
// the mutex behind this pointer also permits safe value-format redaction.
type contactState struct {
	mu                sync.RWMutex
	local             identity.PublicKey
	peer              identity.PublicKey
	revoked           bool
	preparedTransport bool
	completion        *channel.Completion
	contactID         string
}

func (Contact) String() string   { return "[private WhatsUp contact credentials]" }
func (Contact) GoString() string { return "[private WhatsUp contact credentials]" }

func newContact(local, peer identity.PublicKey) (*Contact, error) {
	if !local.Valid() || !peer.Valid() || local.Equal(peer) {
		return nil, ErrPeer
	}
	// Local approval creates no ongoing session secret. The transport adapter is
	// the sole owner of the future contact's node key and PSK; it may prepare that
	// opaque bundle, but cannot activate it without reciprocal authentication.
	return &Contact{state: &contactState{local: local, peer: peer}}, nil
}

func (c *Contact) Local() identity.PublicKey {
	if c == nil || c.state == nil {
		return identity.PublicKey{}
	}
	return c.state.local
}

func (c *Contact) Peer() identity.PublicKey {
	if c == nil || c.state == nil {
		return identity.PublicKey{}
	}
	return c.state.peer
}

// ContactID scopes local history to this exact approved credential bundle.
// A matching peer identity alone must not inherit another pairing's history.
func (c *Contact) ContactID() string {
	if c == nil || c.state == nil {
		return ""
	}
	return c.state.contactID
}

// MatchesCompletion is a backend association check, not restored authority.
// Only authenticated finalization can install this private completion pointer.
func (c *Contact) MatchesCompletion(completion *channel.Completion) bool {
	if c == nil || c.state == nil || completion == nil {
		return false
	}
	c.state.mu.RLock()
	defer c.state.mu.RUnlock()
	return !c.state.revoked && c.state.completion == completion
}

// AuthorizeAuthenticatedPeer must be applied to the actual peer from a completed
// authenticated handshake, on every operation as well as session admission. A
// caller must not treat a claimed key alone as authentication. Live transports
// must close their connections on revocation; this check alone cannot cancel IO.
func (c *Contact) AuthorizeAuthenticatedPeer(peer identity.PublicKey) error {
	return c.WithAuthenticatedPeer(peer, func() error { return nil })
}

// WithAuthenticatedPeer holds authorization across one bounded backend operation.
// Revocation waits for already admitted operations and then blocks all new ones.
// The callback must not call Contact methods, acquire another authorization guard,
// or perform network IO. Callers with their own mutex acquire it before this guard;
// Revoke never acquires a caller's mutex. Live transports still need cancellation.
func (c *Contact) WithAuthenticatedPeer(peer identity.PublicKey, operation func() error) error {
	if c == nil || c.state == nil {
		return ErrPeer
	}
	s := c.state
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.revoked || !s.local.Valid() || !s.peer.Valid() || s.local.Equal(s.peer) || !peer.Equal(s.peer) {
		return ErrPeer
	}
	if operation == nil {
		return ErrPeer
	}
	return operation()
}

// PrepareTransportCredentials runs one bounded backend provisioning operation
// once per local approval. This does not grant reciprocal approval or networking
// permission. The transport owns the resulting opaque credential bundle; pairing
// deliberately has no competing session key or WireGuard PSK. The callback must
// not call Contact methods or perform network IO. Failed preparation may retry.
func (c *Contact) PrepareTransportCredentials(operation func() error) error {
	if c == nil || c.state == nil {
		return ErrPeer
	}
	s := c.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.preparedTransport || !s.local.Valid() || !s.peer.Valid() || s.local.Equal(s.peer) || operation == nil {
		return ErrPeer
	}
	if err := operation(); err != nil {
		return err
	}
	s.preparedTransport = true
	return nil
}

func (c *Contact) Revoke() {
	if c == nil || c.state == nil {
		return
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.revoked = true
}
