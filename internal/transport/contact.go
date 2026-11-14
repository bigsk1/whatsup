package transport

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"

	"whatsup/internal/identity"
	"whatsup/internal/pairing"
)

// ChatPort is the only TCP service this adapter exposes inside the WireGuard
// tunnel. It is not a listening port on the host's public or LAN interface.
const ChatPort = 48731

var (
	ErrInvalidCredentials        = errors.New("contact transport credentials are missing or invalid")
	ErrInvalidPeer               = errors.New("approved transport peer is missing or invalid")
	ErrInvalidRelay              = errors.New("relay map must explicitly use an absolute HTTPS URL without credentials or fragment")
	ErrNotStarted                = errors.New("contact transport has not started")
	ErrAlreadyStarted            = errors.New("contact transport already started")
	ErrPeerRejected              = errors.New("transport peer is not the approved contact")
	ErrConnectionLimit           = errors.New("contact already has an active connection")
	ErrMutualApprovalUnavailable = errors.New("reciprocal authenticated contact activation is unavailable")
	ErrPrivacyUnavailable        = errors.New("contact transport privacy controls are unavailable")
)

// PeerKey is a public WireGuard node key. It is distinct from the application's
// Ed25519 identity. An authenticated pairing protocol must bind the two before
// providing ApprovedPeer to this adapter. A display name cannot provide that binding.
type PeerKey struct{ node key.NodePublic }

func ParsePeerKey(encoded string) (PeerKey, error) {
	var node key.NodePublic
	if len(encoded) != len("nodekey:")+64 || node.UnmarshalText([]byte(encoded)) != nil || node.IsZero() || node.String() != encoded {
		return PeerKey{}, ErrInvalidPeer
	}
	return PeerKey{node: node}, nil
}

// String returns public key material only; no contact PSK or private key is included.
func (p PeerKey) String() string { return p.node.String() }

// ContactCredentials is the sole opaque ongoing credential bundle for a locally
// approved contact. Pairing creates no other session key or PSK. Preparing these
// keys does not activate a contact: a reviewed reciprocal identity-to-node-key
// binding is required before constructing a server. That protocol is not yet
// implemented, so there is deliberately no production activation API.
// Copies share the private state, claim and revocation; there is no secret export
// or persistence interface until encrypted reciprocal activation is reviewed.
type ContactCredentials struct{ state *contactCredentialState }

type contactCredentialState struct {
	mu       sync.Mutex
	approval *pairing.Contact
	node     key.NodePrivate
	psk      tailcat.PresharedKey
	binding  *verifiedBinding
	claimed  bool
	revoked  bool
}

// verifiedBinding can only be populated inside this adapter by the future
// reviewed mutual-authentication implementation. It is not a caller-supplied
// boolean or unverified peer claim. Tests create explicitly synthetic bindings
// to exercise admission independently of the unimplemented protocol.
type verifiedBinding struct {
	localIdentity identity.PublicKey
	peerIdentity  identity.PublicKey
	localNode     key.NodePublic
	peerNode      key.NodePublic
}

// boundTo requires the caller to hold s.mu. This checks the association only;
// it cannot manufacture the absent reciprocal-authentication protocol.
func (s *contactCredentialState) boundTo(peer key.NodePublic) bool {
	binding := s.binding
	return binding != nil && binding.localIdentity == s.approval.Local() && binding.peerIdentity == s.approval.Peer() && binding.localNode == s.node.Public() && binding.peerNode == peer
}

func NewContactCredentials(approval *pairing.Contact) (*ContactCredentials, error) {
	if approval == nil {
		return nil, ErrInvalidCredentials
	}
	state := &contactCredentialState{approval: approval}
	if err := approval.PrepareTransportCredentials(func() error {
		state.node = key.NewNode()
		for state.psk.IsZero() {
			if _, err := rand.Read(state.psk[:]); err != nil {
				state.node = key.NodePrivate{}
				clear(state.psk[:])
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, ErrInvalidCredentials
	}
	return &ContactCredentials{state: state}, nil
}

func (c *ContactCredentials) PeerKey() PeerKey {
	if c == nil || c.state == nil {
		return PeerKey{}
	}
	s := c.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.node.IsZero() || s.approval == nil || s.approval.AuthorizeAuthenticatedPeer(s.approval.Peer()) != nil {
		return PeerKey{}
	}
	return PeerKey{node: s.node.Public()}
}

// Revoke invalidates every copy of the prepared bundle. Live transports also
// need Close to cancel IO; clearing known fields cannot erase prior Go copies.
func (c *ContactCredentials) Revoke() {
	if c == nil || c.state == nil {
		return
	}
	s := c.state
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = true
	s.node = key.NodePrivate{}
	clear(s.psk[:])
	s.binding = nil
}

func (ContactCredentials) String() string   { return "[private contact credentials]" }
func (ContactCredentials) GoString() string { return "[private contact credentials]" }
func (ContactCredentials) MarshalJSON() ([]byte, error) {
	return nil, errors.New("contact credentials cannot be serialized")
}

// ContactAddress is a bearer transport credential because it contains the PSK.
// It must travel only inside the mutually authenticated pairing channel, never
// in logs, a frontend DTO, public DNS, or a group invitation.
type ContactAddress struct{ encoded tailcat.Addr }

func (a ContactAddress) EncodeForApprovedPeer() string { return string(a.encoded) }
func (ContactAddress) String() string                  { return "[private contact address]" }
func (ContactAddress) GoString() string                { return "[private contact address]" }
func (ContactAddress) MarshalJSON() ([]byte, error) {
	return nil, errors.New("contact address cannot be serialized")
}

// ServerOptions are backend-only. Construct them after mutual identity approval,
// not merely after reserving an invitation. Construction does no network work.
type ServerOptions struct {
	Credentials        *ContactCredentials
	ApprovedPeer       PeerKey
	Policy             Policy
	AllowRelayFallback bool
	// Require an explicit operator-selected HTTPS map. Never silently fetch
	// Tailcat's public default or accept this URL from a peer's invitation.
	DERPMapURL string
}

type authenticatedListener interface {
	Accept() (net.Conn, error)
	Close() error
}

// ContactServer admits exactly one approved WireGuard peer and one active stream.
// It does not implement application framing, identity verification, or pairing.
// In particular, successfully constructing it does not prove mutual approval.
type ContactServer struct {
	mu          sync.Mutex
	server      *tailcat.Server
	listener    authenticatedListener
	peerKey     func(net.Addr) (key.NodePublic, bool)
	peer        key.NodePublic
	revoked     atomic.Bool
	started     bool
	closed      bool
	active      *contactConn
	policy      Policy
	consent     bool
	credentials *ContactCredentials
}

func NewContactServer(options ServerOptions) (*ContactServer, error) {
	if err := ValidatePolicy(options.Policy, options.AllowRelayFallback); err != nil {
		return nil, err
	}
	credentials := options.Credentials
	if credentials == nil || credentials.state == nil {
		return nil, ErrInvalidCredentials
	}
	s := credentials.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || s.claimed || s.node.IsZero() || s.psk.IsZero() || s.approval == nil || s.approval.AuthorizeAuthenticatedPeer(s.approval.Peer()) != nil {
		return nil, ErrInvalidCredentials
	}
	peer := options.ApprovedPeer.node
	if peer.IsZero() || peer == s.node.Public() {
		return nil, ErrInvalidPeer
	}
	u, err := url.Parse(options.DERPMapURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, ErrInvalidRelay
	}
	if !s.boundTo(peer) {
		return nil, ErrMutualApprovalUnavailable
	}
	// A credential object belongs to one contact server. Copies share the claim
	// flag, preventing accidental reuse of a PSK between contacts or lifecycles.
	s.claimed = true
	c := &ContactServer{peer: peer, policy: options.Policy, consent: options.AllowRelayFallback, credentials: credentials}
	c.server = &tailcat.Server{
		Key: s.node, PresharedKey: s.psk,
		// Never set DisablePresharedKey. A nil admission hook is fail-open in
		// upstream Tailcat, so install this exact-key predicate before Start.
		AllowClient: c.admits,
		// The listener adds only ChatPort to this empty allowlist. All other
		// callbacks, including forwarding, UDP, and proxy callbacks, stay nil.
		ServedTCPPorts: []filter.PortRange{},
		ServedUDPPorts: []filter.PortRange{},
		DERPMapURL:     options.DERPMapURL,
		// Upstream diagnostic strings can contain addresses. Do not forward them
		// into application logs; provide only generic typed errors to the caller.
		Logf:                        func(string, ...any) {},
		DisableDERPApplicationStats: true,
	}
	c.peerKey = c.server.PeerKey
	return c, nil
}

func (c *ContactServer) admits(peer key.NodePublic) bool {
	if c.revoked.Load() || peer.IsZero() || peer != c.peer {
		return false
	}
	if c.credentials == nil || c.credentials.state == nil {
		return false
	}
	s := c.credentials.state
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.revoked && s.approval != nil && s.boundTo(peer) && s.approval.AuthorizeAuthenticatedPeer(s.approval.Peer()) == nil
}

// Start remains unavailable until ongoing-contact endpoint/reconfirmation and
// operator-profile policy are integrated. Invitation transport is separate.
// Constructing an activated binding alone does not start a messaging tunnel.
func (c *ContactServer) Start(_ context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.revoked.Load() {
		return net.ErrClosed
	}
	if c.started {
		return ErrAlreadyStarted
	}
	if err := ValidatePolicy(c.policy, c.consent); err != nil {
		return err
	}
	if !c.admits(c.peer) {
		return ErrMutualApprovalUnavailable
	}
	// The reviewed fork supplies privacy controls, but this older ongoing adapter
	// still lacks trusted-profile/endpoint restoration. Keep it closed while the
	// dedicated invitation adapter supports only the pairing service.
	return ErrPrivacyUnavailable
}

// AddressForApprovedPeer contains the ongoing session PSK, not invitation
// credentials. Do not expose it through the Wails bridge.
func (c *ContactServer) AddressForApprovedPeer() (ContactAddress, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.revoked.Load() {
		return ContactAddress{}, net.ErrClosed
	}
	if !c.started {
		return ContactAddress{}, ErrNotStarted
	}
	return ContactAddress{encoded: c.server.TailcatAddr()}, nil
}

// Accept checks the peer identity authenticated by WireGuard, even though
// admission was already checked. Revocation wins a race with a pending Accept.
func (c *ContactServer) Accept() (net.Conn, error) {
	c.mu.Lock()
	if c.closed || c.revoked.Load() {
		c.mu.Unlock()
		return nil, net.ErrClosed
	}
	if !c.started {
		c.mu.Unlock()
		return nil, ErrNotStarted
	}
	listener := c.listener
	c.mu.Unlock()
	conn, err := listener.Accept()
	if err != nil {
		return nil, err
	}
	peer, ok := c.peerKey(conn.RemoteAddr())
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.revoked.Load() {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	if !ok || !c.admits(peer) {
		_ = conn.Close()
		return nil, ErrPeerRejected
	}
	if c.active != nil {
		_ = conn.Close()
		return nil, ErrConnectionLimit
	}
	tracked := &contactConn{Conn: conn, owner: c}
	c.active = tracked
	return tracked, nil
}

// Revoke rejects new admissions before dropping the connected peer and closing
// the listener and active stream. The transport cannot be re-approved in place.
func (c *ContactServer) Revoke() error { return c.Close() }

func (c *ContactServer) Close() error {
	c.revoked.Store(true)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	if c.credentials != nil {
		c.credentials.Revoke()
	}
	listener, active := c.listener, c.active
	c.active = nil
	started := c.started
	c.mu.Unlock()
	var errs []error
	if active != nil {
		errs = append(errs, active.Close())
	}
	if listener != nil {
		errs = append(errs, listener.Close())
	}
	if started && c.server != nil {
		c.server.DisconnectClient(c.peer)
		errs = append(errs, c.server.Close())
	}
	return errors.Join(errs...)
}

type contactConn struct {
	net.Conn
	owner *ContactServer
	once  sync.Once
	err   error
}

func (c *contactConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		c.owner.mu.Lock()
		if c.owner.active == c {
			c.owner.active = nil
		}
		c.owner.mu.Unlock()
	})
	return c.err
}

// Compile-time checks keep accidental JSON export of bearer secrets disabled.
var (
	_ json.Marshaler = (*ContactCredentials)(nil)
	_ json.Marshaler = ContactCredentials{}
	_ json.Marshaler = ContactAddress{}
)
