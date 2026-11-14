package storage

import (
	"whatsup/internal/identity"
	"whatsup/internal/relaychange"
)

func retiredRoute(r routeStateDisk, nonce string) bool {
	for _, id := range r.Retired {
		if id == nonce {
			return true
		}
	}
	return false
}
func retireRoute(r *routeStateDisk, nonce string) {
	if !retiredRoute(*r, nonce) {
		r.Retired = append(r.Retired, nonce)
		if len(r.Retired) > 32 {
			r.Retired = append([]string{}, r.Retired[len(r.Retired)-32:]...)
		}
	}
}
func decisionStatement(proposal relaychange.Signed, kind, signer uint8) (relaychange.Statement, error) {
	p, err := proposal.Statement()
	if err != nil {
		return relaychange.Statement{}, ErrRelayChange
	}
	p.Kind, p.Signer, p.ServiceURL, p.RegionID, p.ProposalHash = kind, signer, "", 0, proposal.Digest()
	return p, nil
}
func (a *ApplicationState) ApproveRelayChange(id, requestID string) (RelayChangeSnapshot, error) {
	if a == nil || a.vault == nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, index, err := a.routeLocked(id)
	if err != nil {
		return RelayChangeSnapshot{}, err
	}
	p := r.Pending
	if p == nil || p.RequestID != requestID || p.Proposal == nil {
		return snapshotRoute(base, r), ErrRelayChange
	}
	proposal, err := proposalStatement(base, p.Proposal)
	if err != nil || proposal.Proposer == role(base) {
		return snapshotRoute(base, r), ErrRelayChange
	}
	if approvalValid(base, p.Proposal, p.Approval) {
		return snapshotRoute(base, r), nil
	}
	statement, err := decisionStatement(p.Proposal.signed(), relaychange.Approval, role(base))
	if err != nil {
		return snapshotRoute(base, r), err
	}
	local, _ := identity.FromSeed(a.state.Identity.Seed)
	signed, err := relaychange.Sign(local, statement)
	if err != nil {
		return snapshotRoute(base, r), err
	}
	p.Approval = diskSigned(signed)
	if err := a.saveRouteLocked(r, index); err != nil {
		return RelayChangeSnapshot{}, err
	}
	return snapshotRoute(base, a.routeStateLocked(id)), nil
}
func (a *ApplicationState) DeclineRelayChange(id, requestID string) (RelayChangeSnapshot, error) {
	return a.routeDecision(id, requestID, relaychange.Decline)
}
func (a *ApplicationState) CancelRelayChange(id, requestID string) (RelayChangeSnapshot, error) {
	return a.routeDecision(id, requestID, relaychange.Cancel)
}
func (a *ApplicationState) routeDecision(id, requestID string, kind uint8) (RelayChangeSnapshot, error) {
	if a == nil || a.vault == nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, index, err := a.routeLocked(id)
	if err != nil {
		return RelayChangeSnapshot{}, err
	}
	p := r.Pending
	if p == nil || p.RequestID != requestID || p.LocalCommit != nil {
		return snapshotRoute(base, r), ErrRelayChange
	}
	if p.Proposal == nil {
		if kind != relaychange.Cancel || !p.LocalIntent {
			return snapshotRoute(base, r), ErrRelayChange
		}
		r.Pending = nil
		retireRoute(&r, requestID)
		r.LastState = "declined"
		if err := a.saveRouteLocked(r, index); err != nil {
			return RelayChangeSnapshot{}, err
		}
		return snapshotRoute(base, a.routeStateLocked(id)), nil
	}
	proposal, err := proposalStatement(base, p.Proposal)
	if err != nil {
		return snapshotRoute(base, r), err
	}
	if kind == relaychange.Decline && (proposal.Proposer == role(base) || approvalValid(base, p.Proposal, p.Approval)) {
		return snapshotRoute(base, r), ErrRelayChange
	}
	statement, err := decisionStatement(p.Proposal.signed(), kind, role(base))
	if err != nil {
		return snapshotRoute(base, r), err
	}
	local, _ := identity.FromSeed(a.state.Identity.Seed)
	signed, err := relaychange.Sign(local, statement)
	if err != nil {
		return snapshotRoute(base, r), err
	}
	r.Outgoing = diskSigned(signed)
	r.Pending = nil
	r.LastState = "declined"
	retireRoute(&r, requestID)
	if err := a.saveRouteLocked(r, index); err != nil {
		return RelayChangeSnapshot{}, err
	}
	return snapshotRoute(base, a.routeStateLocked(id)), nil
}
func (a *ApplicationState) ImportRelayChange(id, text string) (RelayChangeSnapshot, error) {
	signed, err := relaychange.Parse(text)
	if err != nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	return a.importRelaySigned(id, signed)
}
func (a *ApplicationState) ImportRelayContribution(id string, raw []byte) (RelayChangeSnapshot, error) {
	signed, err := relaychange.ParseBytes(raw)
	if err != nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	return a.importRelaySigned(id, signed)
}
func (a *ApplicationState) importRelaySigned(id string, signed relaychange.Signed) (RelayChangeSnapshot, error) {
	if a == nil || a.vault == nil {
		return RelayChangeSnapshot{}, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, index, err := a.routeLocked(id)
	if err != nil {
		return RelayChangeSnapshot{}, err
	}
	statement, err := signed.Verify(base.Peer)
	if err != nil || statement.Signer != 3-role(base) || statement.ContactID != id || statement.Base != r.Base || statement.Kind == relaychange.Commit {
		return snapshotRoute(base, r), ErrRelayChange
	}
	if statement.Kind == relaychange.Proposal {
		if retiredRoute(r, statement.Nonce) {
			return snapshotRoute(base, r), nil
		}
		if r.Pending != nil {
			if r.Pending.Proposal != nil && r.Pending.Proposal.signed().Digest() == signed.Digest() {
				return snapshotRoute(base, r), nil
			}
			return snapshotRoute(base, r), ErrRelayChange
		}
		active, err := routeBinding(base, r.Epoch, r.Active)
		if err != nil || statement.Epoch != r.Epoch+1 || statement.Old != active || statement.Proposer != 3-role(base) || ValidateRelayServiceURL(statement.ServiceURL) != nil {
			return snapshotRoute(base, r), ErrRelayChange
		}
		proposed := relayRoute{statement.ServiceURL, statement.RegionID}
		if proposed == r.Active {
			return snapshotRoute(base, r), ErrRelayChange
		}
		if _, err := routeRecord(base, proposed); err != nil {
			return snapshotRoute(base, r), err
		}
		r.Pending = &routePending{RequestID: statement.Nonce, ServiceURL: statement.ServiceURL, Proposal: diskSigned(signed)}
		r.Outgoing = nil
		r.LastState = ""
	} else {
		if r.Pending == nil {
			if r.Last != nil && r.Last.Agreement.Approval != nil && r.Last.Agreement.Approval.signed().Digest() == signed.Digest() {
				return snapshotRoute(base, r), nil
			}
			if retiredRoute(r, statement.Nonce) {
				return snapshotRoute(base, r), nil
			}
			return snapshotRoute(base, r), ErrRelayChange
		}
		p := r.Pending
		if p.Proposal == nil {
			return snapshotRoute(base, r), ErrRelayChange
		}
		proposal, err := proposalStatement(base, p.Proposal)
		if err != nil || !contextEqual(proposal, statement) || statement.ProposalHash != p.Proposal.signed().Digest() {
			return snapshotRoute(base, r), ErrRelayChange
		}
		if statement.Kind == relaychange.Approval {
			if proposal.Proposer != role(base) || !p.LocalIntent {
				return snapshotRoute(base, r), ErrRelayChange
			}
			if approvalValid(base, p.Proposal, p.Approval) {
				if p.Approval.signed().Digest() != signed.Digest() {
					return snapshotRoute(base, r), ErrRelayChange
				}
				return snapshotRoute(base, r), nil
			}
			p.Approval = diskSigned(signed)
		} else {
			if statement.Kind != relaychange.Decline && statement.Kind != relaychange.Cancel {
				return snapshotRoute(base, r), ErrRelayChange
			}
			r.Pending = nil
			r.Outgoing = nil
			r.LastState = "declined"
			retireRoute(&r, statement.Nonce)
		}
	}
	if err := a.saveRouteLocked(r, index); err != nil {
		return RelayChangeSnapshot{}, err
	}
	return snapshotRoute(base, a.routeStateLocked(id)), nil
}
func localContribution(base PendingRecord, r routeStateDisk) *routeSigned {
	if r.Pending != nil && r.Pending.Proposal != nil {
		p, err := proposalStatement(base, r.Pending.Proposal)
		if err != nil {
			return nil
		}
		if p.Proposer == role(base) {
			return r.Pending.Proposal
		}
		return r.Pending.Approval
	}
	if r.Outgoing != nil {
		return r.Outgoing
	}
	if r.Last != nil && r.LastState != "declined" {
		p, err := proposalStatement(base, r.Last.Agreement.Proposal)
		if err != nil {
			return nil
		}
		if p.Proposer == role(base) {
			return r.Last.Agreement.Proposal
		}
		return r.Last.Agreement.Approval
	}
	return nil
}
func (a *ApplicationState) ExportRelayChange(id string) (string, error) {
	if a == nil || a.vault == nil {
		return "", ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, _, err := a.routeLocked(id)
	if err != nil {
		return "", err
	}
	s := localContribution(base, r)
	if s == nil {
		return "", ErrRelayChange
	}
	return relaychange.Format(s.signed())
}
func (a *ApplicationState) RelayContributions(id string) ([][]byte, error) {
	if a == nil || a.vault == nil {
		return nil, ErrRelayChange
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, r, _, err := a.routeLocked(id)
	if err != nil {
		return nil, err
	}
	s := localContribution(base, r)
	if s == nil {
		return [][]byte{}, nil
	}
	return [][]byte{s.signed().Bytes()}, nil
}
