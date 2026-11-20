package transport

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"
)

// CallMediaPort is the only UDP service inside the WireGuard tunnel. Nothing
// serves it until a call has been answered, and nothing serves it afterwards:
// the port exists exactly as long as one call's media flow.
const CallMediaPort = 48732

const callMediaDialTimeout = 10 * time.Second

// ErrCallMedia reports that a call media flow could not be opened or was closed.
var ErrCallMedia = errors.New("call media channel is unavailable")

// A call media flow is a raw datagram path to the approved WireGuard peer. It
// carries no application authority. The caller seals and authenticates every
// datagram with keys taken from the pinned-identity session before any media
// reaches a decoder, exactly as chat requires fresh TLS before any frame.

type serverCallMedia struct {
	net.Conn
	owner    *ongoingServerConn
	listener net.Listener
	extras   chan struct{}
	once     sync.Once
}

func (m *serverCallMedia) Close() error {
	m.once.Do(func() {
		state := m.owner.owner.state
		state.mu.Lock()
		if state.media == m {
			state.media = nil
		}
		conn, listener, extras := m.Conn, m.listener, m.extras
		state.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		if listener != nil {
			_ = listener.Close()
		}
		if extras != nil {
			<-extras
		}
	})
	return nil
}

// OpenCallMedia serves the call media port for this authenticated stream and
// waits for the approved peer's flow. It is the inviter side of every call,
// whoever placed it. Cancelling ctx abandons the wait and closes the port.
// Closing the returned connection, this stream or the listener closes the
// port again. At most one flow exists per stream.
func (c *ongoingServerConn) OpenCallMedia(ctx context.Context) (net.Conn, error) {
	if c == nil || c.owner == nil || c.owner.state == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrCallMedia
	}
	state := c.owner.state
	media := &serverCallMedia{owner: c}
	state.mu.Lock()
	if state.closed || state.active != c || state.media != nil || state.life.Err() != nil {
		state.mu.Unlock()
		return nil, ErrCallMedia
	}
	state.media = media
	server, peer, life := state.server, state.peer, state.life
	state.mu.Unlock()

	listener, err := server.Listen(life, "udp", ":"+strconv.Itoa(CallMediaPort))
	if err != nil {
		_ = media.Close()
		return nil, ErrCallMedia
	}
	state.mu.Lock()
	media.listener = listener
	abandoned := state.media != media
	state.mu.Unlock()
	if abandoned {
		_ = listener.Close()
		return nil, ErrCallMedia
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	var flow net.Conn
	for flow == nil {
		candidate, err := listener.Accept()
		if err != nil {
			stop()
			_ = media.Close()
			return nil, ErrCallMedia
		}
		// Admission already limits the tunnel to the approved node; check the
		// flow itself as well, as the stream path does.
		if got, found := server.PeerKey(candidate.RemoteAddr()); !found || got != peer {
			_ = candidate.Close()
			continue
		}
		flow = candidate
	}
	if !stop() {
		_ = flow.Close()
		_ = media.Close()
		return nil, ErrCallMedia
	}
	extras := make(chan struct{})
	state.mu.Lock()
	if state.closed || state.active != c || state.media != media {
		state.mu.Unlock()
		_ = flow.Close()
		_ = media.Close()
		return nil, ErrCallMedia
	}
	media.Conn, media.extras = flow, extras
	state.mu.Unlock()
	// One flow per call. Any further flow to the open port is closed unread.
	go func() {
		defer close(extras)
		for {
			extra, err := listener.Accept()
			if err != nil {
				return
			}
			_ = extra.Close()
		}
	}()
	return media, nil
}

type clientCallMedia struct {
	net.Conn
	owner *ongoingClientConn
	once  sync.Once
}

func (m *clientCallMedia) Close() error {
	m.once.Do(func() {
		m.owner.mu.Lock()
		if m.owner.media == m {
			m.owner.media = nil
		}
		conn := m.Conn
		m.owner.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
	})
	return nil
}

// OpenCallMedia opens this stream's single media flow to the approved server's
// call media port. It is the joiner side of every call, whoever placed it. The
// flow reaches the peer only once that side is serving the port, so the caller
// repeats its first sealed datagram until an authentic one comes back.
func (c *ongoingClientConn) OpenCallMedia(ctx context.Context) (net.Conn, error) {
	if c == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrCallMedia
	}
	media := &clientCallMedia{owner: c}
	c.mu.Lock()
	if c.closed || c.client == nil || c.media != nil {
		c.mu.Unlock()
		return nil, ErrCallMedia
	}
	c.media = media
	client := c.client
	c.mu.Unlock()
	dial, cancel := context.WithTimeout(ctx, callMediaDialTimeout)
	flow, err := client.DialUDPPort(dial, CallMediaPort)
	cancel()
	if err != nil {
		_ = media.Close()
		return nil, ErrCallMedia
	}
	c.mu.Lock()
	if c.closed || c.media != media || ctx.Err() != nil {
		c.mu.Unlock()
		_ = flow.Close()
		_ = media.Close()
		return nil, ErrCallMedia
	}
	media.Conn = flow
	c.mu.Unlock()
	return media, nil
}
