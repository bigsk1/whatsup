package transport

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tailscale/tailcat"
	"tailscale.com/feature"
	"tailscale.com/feature/buildfeatures"
	"tailscale.com/types/key"
	"tailscale.com/wgengine/filter"

	"whatsup/internal/transport/address"
)

const (
	InvitationPort         = 48730
	maxInvitationPeers     = 4
	maxInvitationLifetime  = 10 * time.Minute
	invitationDialTimeout  = 20 * time.Second
	invitationDrainTimeout = 3 * time.Second
	// Referencing the reviewed patch makes an unpatched dependency fail to build.
	invitationPrivacyPatch = tailcat.WhatsUpPrivacyPatchVersion
)

var ErrInvitationTransport = errors.New("invitation transport is unavailable")

type InvitationOptions struct {
	Profile            *RelayProfile
	Policy             Policy
	AllowRelayFallback bool
}

// InvitationRetirer is implemented only by the owned invitation streams.
// The caller must finish the authenticated terminal protocol first. The joiner
// actively closes its TCP writing side; the inviter retires passively after
// observing raw EOF. Close remains the immediate abort path in every phase.
type InvitationRetirer interface {
	RetireInvitation(ctx context.Context, passive bool) error
}

// InvitationListener owns a distinct ephemeral node key/PSK and one accepted
// stream. It exposes only the fixed invitation TCP service inside WireGuard.
// These credentials never become ongoing contact credentials or enter storage.
type InvitationListener struct{ state *invitationState }

type invitationState struct {
	mu        sync.Mutex
	closeOnce sync.Once
	server    *tailcat.Server
	listener  net.Listener
	endpoint  address.Endpoint
	peers     map[key.NodePublic]bool
	peer      key.NodePublic
	active    *invitationConn
	accepting bool
	accepted  bool
	closed    bool
	retiring  bool
	setupDone chan struct{}
	cancel    context.CancelFunc
	life      context.Context
	stop      func() bool
	closeErr  error
}

func (InvitationListener) String() string               { return "[private WhatsUp invitation transport]" }
func (InvitationListener) GoString() string             { return "[private WhatsUp invitation transport]" }
func (InvitationListener) MarshalJSON() ([]byte, error) { return nil, ErrInvitationTransport }

func invitationOptions(options InvitationOptions) error {
	if err := ValidatePolicy(options.Policy, options.AllowRelayFallback); err != nil {
		return err
	}
	if !networkPrivacyReady || !options.Profile.Ready() || invitationPrivacyPatch != 2 || feature.HookProxyFromEnvironment.GetOrNil() != nil || buildfeatures.HasPortMapper || buildfeatures.HasCaptivePortal {
		return ErrRelayProfile
	}
	return nil
}

// NewInvitationListener starts only after explicit relay consent and a vetted
// operator profile. It never fetches a default map, advertises another service,
// logs addresses, disables the PSK, or claims strict direct-only enforcement.
func NewInvitationListener(ctx context.Context, options InvitationOptions) (*InvitationListener, error) {
	if ctx == nil {
		return nil, ErrInvitationTransport
	}
	if err := invitationOptions(options); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ErrInvitationTransport
	}
	life, cancel := context.WithTimeout(ctx, maxInvitationLifetime)
	state := &invitationState{setupDone: make(chan struct{}), peers: make(map[key.NodePublic]bool), life: life, cancel: cancel}
	owned := &InvitationListener{state: state}
	profile := options.Profile.state
	var err error
	state.server, err = newInvitationServer(options.Profile, owned.admit)
	if err != nil {
		cancel()
		return nil, ErrInvitationTransport
	}
	// Close waits for setupDone before touching upstream state, so cancellation
	// before/during Start cannot race a late Start into an already closed owner.
	state.stop = context.AfterFunc(life, func() { _ = owned.Close() })
	setup := func() error {
		if err := state.server.StartContext(life); err != nil {
			return ErrInvitationTransport
		}
		listener, err := state.server.Listen(life, "tcp", ":"+strconv.Itoa(InvitationPort))
		if err != nil {
			return ErrInvitationTransport
		}
		state.listener = listener
		ci, err := tailcat.ParseAddr(state.server.TailcatAddr())
		if err != nil {
			return ErrInvitationTransport
		}
		// Publish a compact region reference. The joiner must resolve that ID
		// from its separately selected profile, never from peer/default lookup.
		ci.Region, ci.RegionID = nil, profile.defaultRegion
		state.endpoint, err = address.Parse(string(ci.Addr()))
		if err != nil {
			return ErrInvitationTransport
		}
		return nil
	}
	err = setup()
	close(state.setupDone)
	state.mu.Lock()
	closed := state.closed
	state.mu.Unlock()
	if err != nil || closed || life.Err() != nil {
		_ = owned.Close()
		return nil, ErrInvitationTransport
	}
	return owned, nil
}

func newInvitationServer(profile *RelayProfile, admit func(key.NodePublic) bool) (*tailcat.Server, error) {
	if !profile.Ready() || admit == nil {
		return nil, ErrRelayProfile
	}
	var psk tailcat.PresharedKey
	for psk.IsZero() {
		if _, err := rand.Read(psk[:]); err != nil {
			return nil, ErrInvitationTransport
		}
	}
	defer clear(psk[:])
	return &tailcat.Server{
		Key: key.NewNode(), PresharedKey: psk,
		Region:                      cloneRelayRegion(profile.state.regions[profile.state.defaultRegion]),
		DERPMapURL:                  profile.state.source,
		DisableDERPApplicationStats: true,
		AllowClient:                 admit,
		ServedTCPPorts:              []filter.PortRange{}, ServedUDPPorts: []filter.PortRange{},
		Logf: func(string, ...any) {},
	}, nil
}

func (l *InvitationListener) admit(peer key.NodePublic) bool {
	if l == nil || l.state == nil || peer.IsZero() {
		return false
	}
	state := l.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.retiring || state.life.Err() != nil || (state.server != nil && peer == state.server.Key.Public()) {
		return false
	}
	if state.accepted {
		return peer == state.peer
	}
	if state.peers[peer] {
		return true
	}
	if len(state.peers) >= maxInvitationPeers {
		return false
	}
	state.peers[peer] = true
	return true
}

func (l *InvitationListener) AddrEndpoint() (address.Endpoint, error) {
	if l == nil || l.state == nil {
		return address.Endpoint{}, ErrInvitationTransport
	}
	state := l.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.retiring || state.life.Err() != nil || !state.endpoint.Valid() {
		return address.Endpoint{}, net.ErrClosed
	}
	return state.endpoint, nil
}

// Accept permits one authenticated WireGuard stream, not a completed pairing.
// TLS identity proof and atomic bearer redemption remain mandatory afterward.
// Cancellation burns the invitation owner rather than leaving an accept behind.
func (l *InvitationListener) Accept(ctx context.Context) (net.Conn, error) {
	if l == nil || l.state == nil || ctx == nil {
		return nil, ErrInvitationTransport
	}
	state := l.state
	state.mu.Lock()
	if state.closed || state.retiring || state.life.Err() != nil {
		state.mu.Unlock()
		return nil, net.ErrClosed
	}
	if state.accepting || state.accepted {
		state.mu.Unlock()
		return nil, ErrConnectionLimit
	}
	state.accepting = true
	listener := state.listener
	state.mu.Unlock()
	if listener == nil {
		_ = l.Close()
		return nil, ErrInvitationTransport
	}
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	conn, err := listener.Accept()
	if err != nil {
		_ = l.Close()
		return nil, ErrInvitationTransport
	}
	peer, found := state.server.PeerKey(conn.RemoteAddr())
	state.mu.Lock()
	if state.closed || ctx.Err() != nil || state.life.Err() != nil || !found || peer.IsZero() || !state.peers[peer] || state.accepted {
		state.mu.Unlock()
		_ = conn.Close()
		_ = l.Close()
		return nil, ErrInvitationTransport
	}
	state.accepted, state.accepting, state.peer = true, false, peer
	wrapped := &invitationConn{Conn: conn, owner: l}
	state.active = wrapped
	state.mu.Unlock()
	return wrapped, nil
}

func (l *InvitationListener) Close() error {
	if l == nil || l.state == nil {
		return nil
	}
	state := l.state
	state.closeOnce.Do(func() {
		state.mu.Lock()
		state.closed = true
		state.mu.Unlock()
		state.cancel()
		<-state.setupDone
		if state.stop != nil {
			state.stop()
		}
		state.mu.Lock()
		active, listener, server := state.active, state.listener, state.server
		state.endpoint = address.Endpoint{}
		clear(state.peers)
		state.peer = key.NodePublic{}
		state.mu.Unlock()
		if active != nil {
			_ = active.Conn.Close()
		}
		if listener != nil {
			_ = listener.Close()
		}
		if server != nil {
			if server.Close() != nil {
				state.closeErr = ErrInvitationTransport
			}
			server.Key = key.NodePrivate{}
			server.PresharedKey = tailcat.PresharedKey{}
		}
	})
	return state.closeErr
}

type invitationConn struct {
	net.Conn
	owner      *InvitationListener
	once       sync.Once
	eof        atomic.Bool
	retirement invitationRetirement
}

func (c *invitationConn) Read(buffer []byte) (int, error) {
	n, err := c.Conn.Read(buffer)
	if errors.Is(err, io.EOF) {
		c.eof.Store(true)
	}
	return n, err
}

func (c *invitationConn) Close() error {
	var err error
	c.once.Do(func() { _ = c.Conn.Close(); err = c.owner.Close() })
	return err
}

func (c *invitationConn) RetireInvitation(ctx context.Context, passive bool) error {
	return c.retirement.run(ctx, func(work context.Context) error {
		if !passive || !c.eof.Load() || c.owner == nil || c.owner.state == nil {
			return ErrInvitationTransport
		}
		state := c.owner.state
		state.mu.Lock()
		if state.closed || state.retiring || state.life.Err() != nil || state.active != c {
			state.mu.Unlock()
			return ErrInvitationTransport
		}
		// Revoke admission and publication before retaining the existing engine
		// solely to acknowledge already authenticated terminal TCP traffic.
		state.retiring = true
		state.endpoint = address.Endpoint{}
		clear(state.peers)
		listener, server := state.listener, state.server
		state.mu.Unlock()
		if listener == nil || server == nil || listener.Close() != nil || c.Conn.Close() != nil {
			return ErrInvitationTransport
		}
		if server.DrainTCP(work) != nil || work.Err() != nil {
			return ErrInvitationTransport
		}
		state.mu.Lock()
		closed := state.closed
		state.mu.Unlock()
		if closed {
			return ErrInvitationTransport
		}
		return nil
	}, c.Close)
}

// invitationRetirement shares one bounded drain across repeated calls. A caller
// cancellation always retains the immediate abort path, including while another
// caller is already draining. The upper bound is a deadline, never a grace sleep.
type invitationRetirement struct {
	mu   sync.Mutex
	done chan struct{}
	err  error
}

func (r *invitationRetirement) run(ctx context.Context, retire func(context.Context) error, abort func() error) error {
	if ctx == nil {
		_ = abort()
		return ErrInvitationTransport
	}
	work, cancel := context.WithTimeout(ctx, invitationDrainTimeout)
	defer cancel()
	if work.Err() != nil {
		_ = abort()
		return ErrInvitationTransport
	}
	r.mu.Lock()
	if r.done != nil {
		done := r.done
		r.mu.Unlock()
		select {
		case <-done:
			return r.err
		case <-work.Done():
			_ = abort()
			return ErrInvitationTransport
		}
	}
	r.done = make(chan struct{})
	r.mu.Unlock()
	stop := context.AfterFunc(work, func() { _ = abort() })
	err := retire(work)
	stop()
	if work.Err() != nil {
		err = ErrInvitationTransport
	}
	if closeErr := abort(); err == nil && closeErr != nil {
		err = ErrInvitationTransport
	}
	if err != nil {
		err = ErrInvitationTransport
	}
	r.mu.Lock()
	r.err = err
	close(r.done)
	r.mu.Unlock()
	return err
}

// DialInvitation validates the entire peer endpoint against the selected profile
// before constructing a client. Its temporary client identity/PSK are distinct
// from the later agreed contact bundle. The returned connection owns the client.
func DialInvitation(ctx context.Context, endpoint address.Endpoint, profile *RelayProfile, policy Policy, relayApproved bool) (net.Conn, error) {
	if ctx == nil {
		return nil, ErrInvitationTransport
	}
	if err := invitationOptions(InvitationOptions{Profile: profile, Policy: policy, AllowRelayFallback: relayApproved}); err != nil {
		return nil, err
	}
	trusted, err := profile.trustedEndpoint(endpoint)
	if err != nil || ctx.Err() != nil {
		return nil, ErrRelayProfile
	}
	client := &tailcat.Client{Server: trusted, Key: key.NewNode(), Logf: func(string, ...any) {},
		DERPMapURL: profile.state.source, DisableDERPApplicationStats: true}
	dial, cancel := context.WithTimeout(ctx, invitationDialTimeout)
	conn, err := client.DialTCPPort(dial, InvitationPort)
	cancel()
	if err != nil {
		_ = client.Close()
		return nil, ErrInvitationTransport
	}
	owned := &invitationClientConn{Conn: conn, client: client}
	// Keep cancellation tied to the supplied invitation lifetime, not the short
	// dial timeout. Neither a returned connection nor a client may outlive it.
	life, stopLife := context.WithTimeout(ctx, maxInvitationLifetime)
	owned.cancel = stopLife
	owned.mu.Lock()
	owned.stop = context.AfterFunc(life, func() { _ = owned.Close() })
	owned.mu.Unlock()
	if life.Err() != nil {
		_ = owned.Close()
		return nil, ErrInvitationTransport
	}
	return owned, nil
}

type invitationClientConn struct {
	net.Conn
	client     *tailcat.Client
	once       sync.Once
	mu         sync.Mutex
	stop       func() bool
	cancel     context.CancelFunc
	closed     bool
	retirement invitationRetirement
}

func (c *invitationClientConn) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		c.closed = true
		stop := c.stop
		c.mu.Unlock()
		c.cancel()
		if stop != nil {
			stop()
		}
		_ = c.Conn.Close()
		_ = c.client.Close()
		c.client.Key = key.NodePrivate{}
		c.client.Server = ""
	})
	return nil
}

func (c *invitationClientConn) RetireInvitation(ctx context.Context, passive bool) error {
	return c.retirement.run(ctx, func(work context.Context) error {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		if passive || closed {
			return ErrInvitationTransport
		}
		writer, ok := c.Conn.(interface{ CloseWrite() error })
		if !ok {
			return ErrInvitationTransport
		}
		deadline, _ := work.Deadline()
		if c.Conn.SetReadDeadline(deadline) != nil || writer.CloseWrite() != nil {
			return ErrInvitationTransport
		}
		var unexpected [1]byte
		if n, err := c.Conn.Read(unexpected[:]); n != 0 || !errors.Is(err, io.EOF) {
			return ErrInvitationTransport
		}
		if c.Conn.Close() != nil || c.client.DrainTCP(work) != nil || work.Err() != nil {
			return ErrInvitationTransport
		}
		c.mu.Lock()
		closed = c.closed
		c.mu.Unlock()
		if closed {
			return ErrInvitationTransport
		}
		return nil
	}, c.Close)
}
