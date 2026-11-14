package channel

import (
	"context"
	"crypto/subtle"
	"io"
)

// Decision is one explicit local UI choice. Approval requires the complete peer
// fingerprint; closing the decision channel is denial, never implicit consent.
type Decision struct {
	Approve     bool
	Fingerprint string
}

// WaitApproval listens for denial/disconnect immediately while waiting for the
// local user. It reads bounded decision frames and owns no UI or listener.
// A remote approval cannot grant consent without the explicit local decision.
func (r *Reservation) WaitApproval(ctx context.Context, decisions <-chan Decision) (*Approval, error) {
	if r == nil || r.state == nil || ctx == nil {
		return nil, ErrInvalid
	}
	state := r.state
	session := &Session{state: state}
	state.mu.Lock()
	if err := state.liveLocked(); err != nil {
		state.mu.Unlock()
		return nil, err
	}
	if !state.reserved || state.phase != pending {
		state.mu.Unlock()
		return nil, ErrUsed
	}
	state.phase = exchanging
	binding := state.binding
	state.mu.Unlock()
	defer clear(binding[:])
	err := session.operation(ctx, func() error {
		type received struct {
			packet [frameSize]byte
			err    error
		}
		read := make(chan received, 1)
		requests := make(chan struct{}, 1)
		stopReader := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			for {
				select {
				case <-stopReader:
					return
				case <-requests:
					var packet [frameSize]byte
					_, err := io.ReadFull(state.tls, packet[:])
					read <- received{packet: packet, err: err}
					if err != nil {
						return
					}
				}
			}
		}()
		next := func() { requests <- struct{}{} }
		next()
		ok := false
		defer func() {
			close(stopReader)
			if !ok {
				_ = session.Close()
			}
			<-finished
		}()
		var outgoing [frameSize]byte
		copy(outgoing[:8], "WUPAIR01")
		outgoing[9] = byte(state.role)
		copy(outgoing[10:], binding[:])
		defer clear(outgoing[:])
		localVoted, peerVoted, sentReady, peerReady := false, false, false, false
		denying, remoteDeny, ackReceived := false, false, false
		denied := func() error {
			// The frame reader is idle here. Retirement owns TLS reads until
			// both close_notify records and the TCP close have drained.
			if err := session.retireTransport(ctx); err != nil {
				return err
			}
			return ErrDenied
		}
		for {
			if !denying && !remoteDeny && localVoted && peerVoted && !sentReady {
				if err := writePacket(state.tls, makePacket("WUREADY1", 3, state.role, binding, 0)); err != nil {
					return err
				}
				sentReady = true
			}
			if !denying && peerReady && sentReady {
				ok = true
				return nil
			}
			select {
			case <-ctx.Done():
				return ErrCanceled
			case remote := <-read:
				if remote.err != nil {
					return ErrProtocol
				}
				opposite := Inviter
				if state.role == Inviter {
					opposite = Joiner
				}
				if remote.packet[9] != byte(opposite) || subtle.ConstantTimeCompare(remote.packet[10:], binding[:]) != 1 {
					return ErrProtocol
				}
				switch {
				case subtle.ConstantTimeCompare(remote.packet[:8], []byte("WUDENY01")) == 1 && remote.packet[8] == 4:
					if !denying || ackReceived {
						return ErrProtocol
					}
					ackReceived = true
					return denied()
				case subtle.ConstantTimeCompare(remote.packet[:8], []byte("WUREADY1")) == 1 && remote.packet[8] == 3:
					if !localVoted || !peerVoted || peerReady {
						return ErrProtocol
					}
					peerReady = true
					if denying {
						next()
					}
				case subtle.ConstantTimeCompare(remote.packet[:8], []byte("WUPAIR01")) == 1 && remote.packet[8] == 2:
					if remoteDeny {
						return ErrProtocol
					}
					remoteDeny = true
					if denying {
						next() // simultaneous denials must read while ACK writes
					}
					if writePacket(state.tls, makePacket("WUDENY01", 4, state.role, binding, 0)) != nil {
						return ErrProtocol
					}
					if !denying {
						return denied()
					}
				case subtle.ConstantTimeCompare(remote.packet[:8], []byte("WUPAIR01")) == 1 && remote.packet[8] == 1:
					if peerVoted || remoteDeny {
						return ErrProtocol
					}
					peerVoted = true
					next()
				default:
					return ErrProtocol
				}
				clear(remote.packet[:])
			case decision, open := <-decisions:
				consent := open && decision.Approve
				if consent {
					if localVoted {
						return ErrUsed
					}
					expected := state.peer.Fingerprint()
					if len(decision.Fingerprint) != len(expected) || subtle.ConstantTimeCompare([]byte(decision.Fingerprint), []byte(expected)) != 1 {
						return ErrFingerprint
					}
					outgoing[8] = 1
				} else {
					outgoing[8] = 2
					denying = true
					decisions = nil
				}
				if err := writePacket(state.tls, outgoing[:]); err != nil {
					return err
				}
				localVoted = true
			}
		}
	})
	if err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.liveLocked(); err != nil {
		return nil, err
	}
	if state.phase != exchanging {
		return nil, ErrClosed
	}
	state.phase = approved
	return &Approval{state: state}, nil
}
