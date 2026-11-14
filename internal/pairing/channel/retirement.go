package channel

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"time"
)

const maxRetirementTime = 3 * time.Second

// TLS may translate a transport EOF at a record boundary to io.EOF. Track raw
// EOF separately so only an authenticated close_notify completes retirement.
type eofTrackingConn struct {
	net.Conn
	eof atomic.Bool
}

func (c *eofTrackingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err == io.EOF {
		c.eof.Store(true)
	}
	return n, err
}

// retireTransport finishes the authenticated terminal protocol without revoking
// its in-memory proof. Only successful completion may subsequently consume that
// proof; expiry/Close still invalidate it. Close remains the immediate abort.
func (s *Session) retireTransport(ctx context.Context) error {
	if s == nil || s.state == nil || ctx == nil {
		return ErrInvalid
	}
	state := s.state
	state.mu.Lock()
	err := state.liveLocked()
	retired := state.transportRetired
	state.mu.Unlock()
	if err != nil || retired {
		return ErrClosed
	}
	work, cancel := context.WithTimeout(ctx, maxRetirementTime)
	defer cancel()
	limit, _ := work.Deadline()
	if state.raw.SetDeadline(earlier(limit, state.operationDeadline())) != nil {
		return ErrProtocol
	}
	stop := context.AfterFunc(work, func() { _ = s.Close() })
	defer stop()
	readEnd := func() error {
		var extra [1]byte
		n, err := state.tls.Read(extra[:])
		if n != 0 || err != io.EOF || state.tlsInput.eof.Load() {
			return ErrProtocol
		}
		return nil
	}
	if state.role == Joiner {
		if state.tls.CloseWrite() != nil || readEnd() != nil {
			return ErrProtocol
		}
	} else {
		if readEnd() != nil || state.tls.CloseWrite() != nil {
			return ErrProtocol
		}
		// The joiner actively closes TCP. Retain the server's stack through
		// the peer FIN, then drain the passive close rather than TIME-WAIT.
		var extra [1]byte
		if n, err := state.raw.Read(extra[:]); n != 0 || err != io.EOF {
			return ErrProtocol
		}
	}
	if drain, ok := state.raw.(interface {
		RetireInvitation(context.Context, bool) error
	}); ok {
		if drain.RetireInvitation(work, state.role == Inviter) != nil {
			return ErrProtocol
		}
	} else if state.raw.Close() != nil {
		return ErrProtocol
	}
	if work.Err() != nil {
		return ErrCanceled
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.liveLocked(); err != nil {
		return err
	}
	state.transportRetired = true
	return nil
}
