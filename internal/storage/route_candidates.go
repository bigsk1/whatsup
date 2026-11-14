package storage

import (
	"whatsup/internal/identity"
	"whatsup/internal/relaychange"
	"whatsup/internal/sessionauth"
)

type ConnectionPlan struct{ Candidates []RouteCandidate }

func (ConnectionPlan) String() string               { return "[private approved connection plan]" }
func (ConnectionPlan) GoString() string             { return "[private approved connection plan]" }
func (ConnectionPlan) MarshalJSON() ([]byte, error) { return nil, ErrRelayChange }

type RouteCandidate struct{ state *routeCandidateState }
type routeCandidateState struct {
	application *ApplicationState
	base        PendingRecord
	route       relayRoute
	epoch       uint64
	association [32]byte
	request     string
	approval    *localContactReceiptState
}

func (RouteCandidate) String() string               { return "[private approved route candidate]" }
func (RouteCandidate) GoString() string             { return "[private approved route candidate]" }
func (RouteCandidate) MarshalJSON() ([]byte, error) { return nil, ErrRelayChange }
func (c RouteCandidate) Association() [32]byte {
	if c.state == nil {
		return [32]byte{}
	}
	return c.state.association
}
func (c RouteCandidate) Epoch() uint64 {
	if c.state == nil {
		return 0
	}
	return c.state.epoch
}
func (c RouteCandidate) RequestID() string {
	if c.state == nil {
		return ""
	}
	return c.state.request
}
func (c RouteCandidate) validateLocked() (PendingRecord, routeStateDisk, int, error) {
	if c.state == nil || c.state.application == nil {
		return PendingRecord{}, routeStateDisk{}, -1, ErrRelayChange
	}
	s := c.state
	base, r, index, err := s.application.routeLocked(s.base.ID)
	if err != nil {
		return base, r, index, err
	}
	if base != s.base || s.approval == nil || s.application.localReceipts[base.ID] != s.approval {
		return base, r, index, ErrRelayChange
	}
	binding, err := routeBinding(base, s.epoch, s.route)
	if err != nil || binding != s.association {
		return base, r, index, ErrRelayChange
	}
	if r.Epoch == s.epoch && r.Active == s.route {
		if r.Epoch > 0 {
			p, err := proposalStatement(base, r.Agreement.Proposal)
			if err != nil || p.Nonce != s.request {
				return base, r, index, ErrRelayChange
			}
		}
		return base, r, index, nil
	}
	if p := r.Pending; p != nil && approvalValid(base, p.Proposal, p.Approval) {
		proposal, err := proposalStatement(base, p.Proposal)
		if err == nil && proposal.Epoch == s.epoch && proposal.Nonce == s.request && proposal.ServiceURL == s.route.ServiceURL && proposal.RegionID == s.route.RegionID {
			return base, r, index, nil
		}
	}
	return base, r, index, ErrRelayChange
}
func (c RouteCandidate) Validate() error {
	if c.state == nil || c.state.application == nil || c.state.application.vault == nil {
		return ErrRelayChange
	}
	a := c.state.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	_, _, _, err := c.validateLocked()
	return err
}
func (c RouteCandidate) Record() (PendingRecord, error) {
	if err := c.Validate(); err != nil {
		return PendingRecord{}, err
	}
	return routeRecord(c.state.base, c.state.route)
}
func makeCandidate(a *ApplicationState, base PendingRecord, epoch uint64, r relayRoute, request string) (RouteCandidate, error) {
	binding, err := routeBinding(base, epoch, r)
	if err != nil {
		return RouteCandidate{}, err
	}
	return RouteCandidate{&routeCandidateState{application: a, base: base, route: r, epoch: epoch, association: binding, request: request, approval: a.localReceipts[base.ID]}}, nil
}
func (a *ApplicationState) ConnectionPlan(id string) (ConnectionPlan, error) {
	if a == nil || a.vault == nil {
		return ConnectionPlan{}, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, _, err := a.routeLocked(id)
	if err != nil {
		return ConnectionPlan{}, err
	}
	var candidates []RouteCandidate
	if p := r.Pending; p != nil && approvalValid(base, p.Proposal, p.Approval) {
		proposal, err := proposalStatement(base, p.Proposal)
		if err != nil {
			return ConnectionPlan{}, err
		}
		candidate, err := makeCandidate(a, base, proposal.Epoch, relayRoute{proposal.ServiceURL, proposal.RegionID}, proposal.Nonce)
		if err != nil {
			return ConnectionPlan{}, err
		}
		candidates = append(candidates, candidate)
	}
	request := ""
	if r.Agreement != nil {
		p, err := proposalStatement(base, r.Agreement.Proposal)
		if err != nil {
			return ConnectionPlan{}, err
		}
		request = p.Nonce
	}
	active, err := makeCandidate(a, base, r.Epoch, r.Active, request)
	if err != nil {
		return ConnectionPlan{}, err
	}
	return ConnectionPlan{Candidates: append(candidates, active)}, nil
}

type RouteSessionProof struct{ state *routeSessionProofState }
type routeSessionProofState struct {
	candidate RouteCandidate
	session   *sessionauth.Session
}

func (RouteSessionProof) String() string               { return "[private live route proof]" }
func (RouteSessionProof) GoString() string             { return "[private live route proof]" }
func (RouteSessionProof) MarshalJSON() ([]byte, error) { return nil, ErrRelayChange }
func (c RouteCandidate) sessionLive(session *sessionauth.Session) error {
	if c.state == nil || session == nil {
		return ErrRelayChange
	}
	s := c.state
	if !session.Matches(s.base.Local, s.base.Peer, s.base.ID, s.association) {
		return ErrRelayChange
	}
	if session.WithLivePeer(s.base.Peer, func() error { return nil }) != nil {
		return ErrRelayChange
	}
	return nil
}
func (c RouteCandidate) Authenticated(session *sessionauth.Session) (RouteSessionProof, error) {
	if err := c.sessionLive(session); err != nil {
		return RouteSessionProof{}, err
	}
	if err := c.Validate(); err != nil {
		return RouteSessionProof{}, err
	}
	return RouteSessionProof{&routeSessionProofState{c, session}}, nil
}
func (p RouteSessionProof) Validate() error {
	if p.state == nil {
		return ErrRelayChange
	}
	if err := p.state.candidate.sessionLive(p.state.session); err != nil {
		return err
	}
	return p.state.candidate.Validate()
}
func (a *ApplicationState) proofLocked(proof RouteSessionProof) (PendingRecord, routeStateDisk, int, error) {
	if proof.state == nil || proof.state.candidate.state == nil || proof.state.candidate.state.application != a {
		return PendingRecord{}, routeStateDisk{}, -1, ErrRelayChange
	}
	return proof.state.candidate.validateLocked()
}
func pendingProof(proof RouteSessionProof, r routeStateDisk) bool {
	if proof.state == nil || r.Pending == nil || r.Pending.Proposal == nil {
		return false
	}
	p, err := r.Pending.Proposal.signed().Statement()
	s := proof.state.candidate.state
	return err == nil && s != nil && s.epoch == p.Epoch && s.request == p.Nonce
}
func (a *ApplicationState) NoteRouteAuthenticated(proof RouteSessionProof) (RelayChangeSnapshot, error) {
	if a == nil || a.vault == nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	if err := proof.Validate(); err != nil {
		return RelayChangeSnapshot{}, err
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, index, err := a.proofLocked(proof)
	if err != nil {
		return RelayChangeSnapshot{}, err
	}
	if !pendingProof(proof, r) || r.Pending.Authenticated {
		return snapshotRoute(base, r), nil
	}
	r.Pending.Authenticated = true
	if err := a.saveRouteLocked(r, index); err != nil {
		return RelayChangeSnapshot{}, err
	}
	return snapshotRoute(base, a.routeStateLocked(base.ID)), nil
}
func signRouteCommit(a *ApplicationState, base PendingRecord, proposal *routeSigned, binding [32]byte) (*routeSigned, error) {
	statement, err := decisionStatement(proposal.signed(), relaychange.Commit, role(base))
	if err != nil {
		return nil, err
	}
	statement.Association = binding
	local, err := identity.FromSeed(a.state.Identity.Seed)
	if err != nil {
		return nil, err
	}
	signed, err := relaychange.Sign(local, statement)
	if err != nil {
		return nil, err
	}
	return diskSigned(signed), nil
}
func (a *ApplicationState) CreateRelayCommit(proof RouteSessionProof) ([]byte, error) {
	if a == nil || a.vault == nil {
		return nil, ErrRelayChange
	}
	if err := proof.Validate(); err != nil {
		return nil, err
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, index, err := a.proofLocked(proof)
	if err != nil {
		return nil, err
	}
	if !pendingProof(proof, r) {
		if r.Last != nil && proof.state.candidate.Epoch() == r.Epoch {
			return r.Last.LocalCommit.signed().Bytes(), nil
		}
		return nil, ErrRelayChange
	}
	if r.Pending.LocalCommit != nil {
		return r.Pending.LocalCommit.signed().Bytes(), nil
	}
	commit, err := signRouteCommit(a, base, r.Pending.Proposal, proof.state.candidate.Association())
	if err != nil {
		return nil, err
	}
	r.Pending.Authenticated = true
	r.Pending.LocalCommit = commit
	if err := a.saveRouteLocked(r, index); err != nil {
		return nil, err
	}
	return commit.signed().Bytes(), nil
}
func commitValid(base PendingRecord, proposal, commit *routeSigned, binding [32]byte, signer uint8) bool {
	p, err := proposalStatement(base, proposal)
	if err != nil || commit == nil {
		return false
	}
	s, err := commit.signed().Verify(keyForRole(base, signer))
	return err == nil && s.Kind == relaychange.Commit && s.Signer == signer && contextEqual(p, s) && s.ProposalHash == proposal.signed().Digest() && s.Association == binding
}
func (a *ApplicationState) AcceptRelayCommit(proof RouteSessionProof, raw []byte) (RelayChangeSnapshot, error) {
	if a == nil || a.vault == nil || len(raw) > 512 {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	if err := proof.Validate(); err != nil {
		return RelayChangeSnapshot{}, err
	}
	signed, err := relaychange.ParseBytes(raw)
	if err != nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, index, err := a.proofLocked(proof)
	if err != nil {
		return RelayChangeSnapshot{}, err
	}
	if !pendingProof(proof, r) {
		if r.Last != nil && r.Last.PeerCommit.signed().Digest() == signed.Digest() {
			return snapshotRoute(base, r), nil
		}
		return snapshotRoute(base, r), ErrRelayChange
	}
	p := r.Pending
	if !approvalValid(base, p.Proposal, p.Approval) || !commitValid(base, p.Proposal, diskSigned(signed), proof.state.candidate.Association(), 3-role(base)) {
		return snapshotRoute(base, r), ErrRelayChange
	}
	if p.LocalCommit == nil {
		p.LocalCommit, err = signRouteCommit(a, base, p.Proposal, proof.state.candidate.Association())
		if err != nil {
			return snapshotRoute(base, r), err
		}
	}
	proposal, err := proposalStatement(base, p.Proposal)
	if err != nil {
		return snapshotRoute(base, r), err
	}
	r.Last = &routeCompletion{Agreement: routeAgreement{diskSigned(p.Proposal.signed()), diskSigned(p.Approval.signed())}, LocalCommit: diskSigned(p.LocalCommit.signed()), PeerCommit: diskSigned(signed)}
	r.Agreement = &routeAgreement{diskSigned(p.Proposal.signed()), diskSigned(p.Approval.signed())}
	r.Epoch = proposal.Epoch
	r.Active = relayRoute{proposal.ServiceURL, proposal.RegionID}
	retireRoute(&r, p.RequestID)
	r.Pending = nil
	r.Outgoing = nil
	r.LastState = ""
	if err := a.saveRouteLocked(r, index); err != nil {
		return RelayChangeSnapshot{}, err
	}
	return snapshotRoute(base, a.routeStateLocked(base.ID)), nil
}
