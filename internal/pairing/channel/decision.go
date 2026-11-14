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
// local user. It reads only one bounded decision frame and owns no UI or listener.
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
			ready  bool
		}
		read := make(chan received, 2)
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			var packet [frameSize]byte
			_, err := io.ReadFull(state.tls, packet[:])
			read <- received{packet: packet, err: err}
			if err != nil || packet[8] != 1 {
				return
			}
			var ready [frameSize]byte
			_, err = io.ReadFull(state.tls, ready[:])
			read <- received{packet: ready, err: err, ready: true}
		}()
		ok := false
		defer func() {
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
		for !peerReady || !sentReady {
			if localVoted && peerVoted && !sentReady {
				if err := writePacket(state.tls, makePacket("WUREADY1", 3, state.role, binding, 0)); err != nil {
					return err
				}
				sentReady = true
			}
			select {
			case <-ctx.Done():
				return ErrCanceled
			case remote := <-read:
				if remote.err != nil {
					return ErrProtocol
				}
				if remote.ready {
					read = nil
					opposite := Inviter
					if state.role == Inviter {
						opposite = Joiner
					}
					if !localVoted || !peerVoted || subtle.ConstantTimeCompare(remote.packet[:8], []byte("WUREADY1")) != 1 || remote.packet[8] != 3 || remote.packet[9] != byte(opposite) || subtle.ConstantTimeCompare(remote.packet[10:], binding[:]) != 1 {
						return ErrProtocol
					}
					peerReady = true
					continue
				}
				if err := validateDecision(remote.packet, outgoing, binding, state.role); err != nil {
					clear(remote.packet[:])
					return err
				}
				clear(remote.packet[:])
				peerVoted = true
			case decision, open := <-decisions:
				decisions = nil
				consent := open && decision.Approve
				if consent {
					expected := state.peer.Fingerprint()
					if len(decision.Fingerprint) != len(expected) || subtle.ConstantTimeCompare([]byte(decision.Fingerprint), []byte(expected)) != 1 {
						return ErrFingerprint
					}
					outgoing[8] = 1
				} else {
					outgoing[8] = 2
				}
				if err := writePacket(state.tls, outgoing[:]); err != nil {
					return err
				}
				localVoted = true
				if !consent {
					return ErrDenied
				}
			}
		}
		ok = true
		return nil
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
