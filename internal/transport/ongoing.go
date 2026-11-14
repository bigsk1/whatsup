package transport

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"

	"whatsup/internal/storage"
	"whatsup/internal/transport/address"
)

var ErrOngoingTransport = errors.New("approved contact connection is unavailable")

// OngoingOptions grants only access to the fixed encrypted bootstrap stream.
// A stored record is not proof of a live peer. The caller must complete fresh
// mutually pinned identity TLS before exposing chat, history or acknowledgments.
type OngoingOptions struct {
	Record             storage.PendingRecord
	Profile            *RelayProfile
	Policy             Policy
	AllowRelayFallback bool
}

type ongoingMaterial struct {
	private  key.NodePrivate
	peer     key.NodePublic
	psk      tailcat.PresharedKey
	endpoint address.Endpoint
	trusted  tailcat.Addr
}

// ongoingOptions checks durable operator choice, exact node/PSK associations and
// the selected map before constructing a Tailcat engine. Peer data cannot add a
// provider or change the approved server address during reconnection.
func ongoingOptions(options OngoingOptions) (ongoingMaterial, error) {
	var material ongoingMaterial
	if err := invitationOptions(InvitationOptions{Profile: options.Profile, Policy: options.Policy, AllowRelayFallback: options.AllowRelayFallback}); err != nil {
		return material, err
	}
	record := options.Record
	if storage.ValidatePendingRecord(record) != nil || record.Version != 2 || !record.RelayFallbackConsent || record.RelayServiceURL != options.Profile.state.source {
		return material, ErrOngoingTransport
	}
	endpoint, err := address.Parse(record.ServerEndpoint)
	if err != nil {
		return material, ErrOngoingTransport
	}
	ci, err := tailcat.ParseAddr(endpoint.EncodeForTransport())
	if err != nil || len(ci.Region) != 0 || !ci.RegionID.IsValid() || options.Profile.state.regions[ci.RegionID] == nil {
		return material, ErrOngoingTransport
	}
	serverNode := record.PeerNode
	if record.Role == "inviter" {
		serverNode = record.LocalNode
	}
	if ci.ServerPublic.String() != serverNode || material.private.UnmarshalText([]byte(record.LocalNodePrivate)) != nil || material.peer.UnmarshalText([]byte(record.PeerNode)) != nil {
		return ongoingMaterial{}, ErrOngoingTransport
	}
	psk, err := base64.RawURLEncoding.Strict().DecodeString(record.PresharedKey)
	defer clear(psk)
	if err != nil || len(psk) != len(material.psk) || subtle.ConstantTimeCompare(psk, ci.PresharedKey[:]) != 1 {
		return ongoingMaterial{}, ErrOngoingTransport
	}
	copy(material.psk[:], psk)
	if record.Role == "inviter" && ci.ServerDiscoPublic != tailcat.DiscoPublicForNode(material.private) {
		return ongoingMaterial{}, ErrOngoingTransport
	}
	trusted, err := options.Profile.trustedEndpoint(endpoint)
	if err != nil {
		return ongoingMaterial{}, ErrOngoingTransport
	}
	material.endpoint, material.trusted = endpoint, trusted
	return material, nil
}

// OngoingListener is a single-contact transport, not authenticated chat authority.
// Only the agreed WireGuard peer reaches ChatPort; every new raw stream still
// requires the caller's fresh pinned TLS session before application access.
type OngoingListener struct{ state *ongoingState }

type ongoingState struct {
	mu        sync.Mutex
	once      sync.Once
	server    *tailcat.Server
	listener  net.Listener
	peer      key.NodePublic
	endpoint  address.Endpoint
	active    *ongoingServerConn
	accepting bool
	closed    bool
	setupDone chan struct{}
	life      context.Context
	cancel    context.CancelFunc
	stop      func() bool
	err       error
}

func (OngoingListener) String() string               { return "[private WhatsUp contact listener]" }
func (OngoingListener) GoString() string             { return "[private WhatsUp contact listener]" }
func (OngoingListener) MarshalJSON() ([]byte, error) { return nil, ErrOngoingTransport }

func NewOngoingListener(ctx context.Context, options OngoingOptions) (*OngoingListener, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrOngoingTransport
	}
	material, err := ongoingOptions(options)
	if err != nil {
		return nil, err
	}
	if options.Record.Role != "inviter" {
		return nil, ErrOngoingTransport
	}
	life, cancel := context.WithCancel(ctx)
	state := &ongoingState{peer: material.peer, endpoint: material.endpoint, setupDone: make(chan struct{}), life: life, cancel: cancel}
	owned := &OngoingListener{state: state}
	// Select the region bound into the stored endpoint, not a new map default.
	ci, _ := tailcat.ParseAddr(material.endpoint.EncodeForTransport())
	state.server = &tailcat.Server{Key: material.private, PresharedKey: material.psk,
		Region: cloneRelayRegion(options.Profile.state.regions[ci.RegionID]), DERPMapURL: options.Record.RelayServiceURL,
		DisableDERPApplicationStats: true, AllowClient: owned.admit,
		ServedTCPPorts: []filter.PortRange{}, ServedUDPPorts: []filter.PortRange{}, Logf: func(string, ...any) {}}
	state.stop = context.AfterFunc(life, func() { _ = owned.Close() })
	err = state.server.StartContext(life)
	if err == nil {
		state.listener, err = state.server.Listen(life, "tcp", ":"+strconv.Itoa(ChatPort))
	}
	close(state.setupDone)
	state.mu.Lock()
	closed := state.closed
	state.mu.Unlock()
	if err != nil || closed || life.Err() != nil {
		_ = owned.Close()
		return nil, ErrOngoingTransport
	}
	return owned, nil
}

func (l *OngoingListener) admit(peer key.NodePublic) bool {
	if l == nil || l.state == nil || peer.IsZero() {
		return false
	}
	state := l.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return !state.closed && state.life.Err() == nil && peer == state.peer
}

func (l *OngoingListener) AddrEndpoint() (address.Endpoint, error) {
	if l == nil || l.state == nil {
		return address.Endpoint{}, ErrOngoingTransport
	}
	state := l.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.life.Err() != nil {
		return address.Endpoint{}, net.ErrClosed
	}
	return state.endpoint, nil
}

// Accept owns one raw stream at a time and permits a subsequent stream after
// Close. Canceling a pending Accept retires this listener; recreating it uses
// the same stored keys/address. This does not change the approved identities.
func (l *OngoingListener) Accept(ctx context.Context) (net.Conn, error) {
	if l == nil || l.state == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrOngoingTransport
	}
	state := l.state
	state.mu.Lock()
	if state.closed || state.life.Err() != nil {
		state.mu.Unlock()
		return nil, net.ErrClosed
	}
	if state.accepting || state.active != nil {
		state.mu.Unlock()
		return nil, ErrConnectionLimit
	}
	state.accepting = true
	listener := state.listener
	state.mu.Unlock()
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	conn, err := listener.Accept()
	if err != nil {
		state.mu.Lock()
		state.accepting = false
		state.mu.Unlock()
		return nil, ErrOngoingTransport
	}
	peer, found := state.server.PeerKey(conn.RemoteAddr())
	state.mu.Lock()
	state.accepting = false
	if state.closed || ctx.Err() != nil || state.life.Err() != nil || !found || peer != state.peer || state.active != nil {
		state.mu.Unlock()
		_ = conn.Close()
		return nil, ErrOngoingTransport
	}
	owned := &ongoingServerConn{Conn: conn, owner: l}
	state.active = owned
	state.mu.Unlock()
	return owned, nil
}

func (l *OngoingListener) Close() error {
	if l == nil || l.state == nil {
		return nil
	}
	state := l.state
	state.once.Do(func() {
		state.mu.Lock()
		state.closed = true
		state.mu.Unlock()
		state.cancel()
		<-state.setupDone
		state.stop()
		state.mu.Lock()
		active, listener, server := state.active, state.listener, state.server
		state.active, state.endpoint, state.peer = nil, address.Endpoint{}, key.NodePublic{}
		state.mu.Unlock()
		if active != nil {
			_ = active.Conn.Close()
		}
		if listener != nil {
			_ = listener.Close()
		}
		if server.Close() != nil {
			state.err = ErrOngoingTransport
		}
		server.Key, server.PresharedKey = key.NodePrivate{}, tailcat.PresharedKey{}
	})
	return state.err
}

type ongoingServerConn struct {
	net.Conn
	owner *OngoingListener
	once  sync.Once
	err   error
}

func (c *ongoingServerConn) Close() error {
	c.once.Do(func() {
		c.err = c.Conn.Close()
		state := c.owner.state
		state.mu.Lock()
		if state.active == c {
			state.active = nil
		}
		state.mu.Unlock()
	})
	return c.err
}

// DialOngoing restores only the agreed WireGuard bootstrap credentials. A
// successful dial is not authenticated contact/history authority; fresh identity
// TLS is mandatory before the returned stream is used for application frames.
func DialOngoing(ctx context.Context, record storage.PendingRecord, profile *RelayProfile, policy Policy, relayApproved bool) (net.Conn, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, ErrOngoingTransport
	}
	material, err := ongoingOptions(OngoingOptions{Record: record, Profile: profile, Policy: policy, AllowRelayFallback: relayApproved})
	if err != nil {
		return nil, err
	}
	if record.Role != "joiner" {
		return nil, ErrOngoingTransport
	}
	client := &tailcat.Client{Server: material.trusted, Key: material.private, DERPMapURL: record.RelayServiceURL,
		DisableDERPApplicationStats: true, Logf: func(string, ...any) {}}
	dial, cancel := context.WithTimeout(ctx, 20*time.Second)
	conn, err := client.DialTCPPort(dial, ChatPort)
	cancel()
	if err != nil {
		_ = client.Close()
		return nil, ErrOngoingTransport
	}
	life, stopLife := context.WithCancel(ctx)
	owned := &ongoingClientConn{Conn: conn, client: client, peer: material.peer, cancel: stopLife}
	owned.mu.Lock()
	owned.stop = context.AfterFunc(life, func() { _ = owned.Close() })
	owned.mu.Unlock()
	if life.Err() != nil {
		_ = owned.Close()
		return nil, ErrOngoingTransport
	}
	return owned, nil
}

type ongoingClientConn struct {
	net.Conn
	client *tailcat.Client
	once   sync.Once
	mu     sync.Mutex
	peer   key.NodePublic
	closed bool
	stop   func() bool
	cancel context.CancelFunc
}

// Close is an immediate local abort. The authenticated session layer owns
// heartbeats/read deadlines so a lost FIN cannot leave a peer waiting forever.
func (c *ongoingClientConn) Close() error {
	c.once.Do(func() {
		c.cancel()
		c.mu.Lock()
		c.closed = true
		stop := c.stop
		c.mu.Unlock()
		stop()
		_ = c.Conn.Close()
		_ = c.client.Close()
		c.client.Key, c.client.Server = key.NodePrivate{}, ""
	})
	return nil
}
