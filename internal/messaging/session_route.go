package messaging

import (
	"context"
	"errors"
	"net"

	"whatsup/internal/diagnostics"
	"whatsup/internal/pairing"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
)

// session is the historical A0 test seam. Production workers always pass the
// particular approved candidate whose transport they opened to sessionRoute.
func (m *Manager) session(raw net.Conn) error {
	routes, err := m.approvedRoutes()
	if err != nil {
		_ = raw.Close()
		return err
	}
	m.mu.Lock()
	m.planKey = routePlanKey(routes)
	m.preferredRoute = routes[0].association
	m.mu.Unlock()
	for _, route := range routes {
		if route.epoch == 0 {
			return m.sessionRoute(m.ctx, raw, route)
		}
	}
	_ = raw.Close()
	return ErrUnavailable
}

func (m *Manager) sessionRoute(parent context.Context, raw net.Conn, route approvedRoute) error {
	return m.sessionRouteCandidates(parent, raw, route)
}

func (m *Manager) sessionRouteCandidates(parent context.Context, raw net.Conn, route approvedRoute, alternatives ...approvedRoute) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	owned := &routeAttempt{raw: raw}
	m.mu.Lock()
	if m.closed || m.peerRemoved || m.removing || m.refreshLocked() != nil || len(m.attempts) >= 2 {
		m.mu.Unlock()
		_ = raw.Close()
		return ErrUnavailable
	}
	if m.attempts == nil {
		m.attempts = map[*routeAttempt]struct{}{}
	}
	m.attempts[owned] = struct{}{}
	m.mu.Unlock()
	defer func() { _ = raw.Close(); m.mu.Lock(); delete(m.attempts, owned); m.mu.Unlock() }()
	if err := route.validate(); err != nil {
		return routeFailure(err)
	}
	options := sessionauth.Options{Local: m.local, Peer: m.record.Peer, ContactID: m.record.ID, Association: route.association, Server: m.record.Role == "inviter"}
	var session *sessionauth.Session
	var err error
	if len(alternatives) == 0 {
		session, err = sessionauth.Authenticate(ctx, raw, options)
	} else {
		if !options.Server || len(alternatives) != 1 || alternatives[0].validate() != nil {
			return ErrUnavailable
		}
		session, err = sessionauth.AuthenticateCandidates(ctx, raw, options, [][32]byte{route.association, alternatives[0].association})
	}
	if err != nil {
		if ctx.Err() == nil {
			if errors.Is(err, sessionauth.ErrIdentityChanged) {
				m.report(diagnostics.IdentityChanged)
			} else if !errors.Is(err, sessionauth.ErrIncompatible) {
				m.report(diagnostics.AuthenticationFailed)
			}
		}
		return err
	}
	defer session.Close()
	if !session.Matches(m.local.PublicKey(), m.record.Peer, m.record.ID, route.association) {
		matched := false
		for _, alternative := range alternatives {
			if session.Matches(m.local.PublicKey(), m.record.Peer, m.record.ID, alternative.association) {
				route, matched = alternative, true
				break
			}
		}
		if !matched {
			return ErrUnavailable
		}
	}
	if route.epoch > 0 && !session.Capabilities().Control {
		return sessionauth.ErrIncompatible
	}
	var proof *storage.RouteSessionProof
	if route.candidate != nil {
		authenticated, err := route.candidate.Authenticated(session)
		if err != nil {
			return routeFailure(err)
		}
		proof = &authenticated
	}
	claim := &routeClaim{attempt: owned, route: route}
	m.mu.Lock()
	if m.closed || m.peerRemoved || m.removing || m.claim != nil {
		m.mu.Unlock()
		return errRouteBusy
	}
	m.claim = claim
	m.raw = raw
	m.mu.Unlock()
	defer func() {
		_ = session.Close()
		m.mu.Lock()
		// A losing/retired handshake can never clear a replacement session.
		if m.claim == claim {
			m.claim = nil
			m.raw = nil
			m.sessionActive = false
			m.capabilities = sessionauth.Capabilities{}
			m.controls = nil
			m.clearPathLocked()
		}
		m.mu.Unlock()
	}()
	if route.candidate == nil {
		// Candidate eligibility requires pre-existing durable local approval.
		// Only unfinished A0 setup may create it via the old exact-A0 proof.
		contact, err := pairing.ReconfirmAuthenticated(session, m.record)
		if err != nil {
			return err
		}
		m.mu.Lock()
		if m.closed || m.peerRemoved || m.removing || m.claim != claim {
			m.mu.Unlock()
			contact.Revoke()
			return ErrUnavailable
		}
		if m.conversation == nil {
			if m.application.ApproveCommittedConversation(m.record, contact) != nil || m.openConversationLocked(contact) != nil {
				m.failLocked()
				m.mu.Unlock()
				contact.Revoke()
				return ErrUnavailable
			}
		} else {
			contact.Revoke()
		}
		m.mu.Unlock()
		plan, err := m.application.ConnectionPlan(m.record.ID)
		if err != nil {
			return routeFailure(err)
		}
		for i := range plan.Candidates {
			candidate := plan.Candidates[i]
			if candidate.Association() == route.association {
				authenticated, err := candidate.Authenticated(session)
				if err != nil {
					return routeFailure(err)
				}
				route.candidate = &candidate
				proof = &authenticated
				break
			}
		}
		if proof == nil {
			return errRoutesChanged
		}
	}
	if _, err := m.application.NoteRouteAuthenticated(*proof); err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed || m.peerRemoved || m.removing || m.claim != claim {
		m.mu.Unlock()
		return ErrUnavailable
	}
	conversation := m.conversation
	if conversation == nil {
		m.mu.Unlock()
		return ErrUnavailable
	}
	if err := conversation.ResetRetries(m.record.Peer); err != nil {
		m.mu.Unlock()
		return err
	}
	m.authenticated++
	if route.association == m.preferredRoute {
		m.preferredAuthenticated++
		// Only success on the currently preferred route is handover progress.
		// An old fallback authenticating again cannot replenish peer churn.
		m.peerRouteBudget = routeReadyRetry{}
	}
	m.state = "connected"
	m.sessionActive = true
	m.capabilities = session.Capabilities()
	m.controls = &controlSession{requests: make(chan *controlRequest, 1), closed: make(chan struct{}), session: session, route: route, proof: proof}
	m.bumpLocked()
	m.mu.Unlock()
	return m.exchange(ctx, session, conversation)
}
