package storage

import (
	"math"
	"whatsup/internal/relaychange"
)

func validateRouteStates(state applicationEnvelope) error {
	seen := map[string]bool{}
	for _, r := range state.Routes {
		if seen[r.ContactID] || r.Revision == 0 || r.Revision == math.MaxUint64 || r.Retired == nil || len(r.Retired) > 32 || r.LastState != "" && r.LastState != "declined" {
			return ErrState
		}
		seen[r.ContactID] = true
		var base PendingRecord
		for _, record := range state.CommittedContacts {
			if record.ID == r.ContactID {
				base = PendingRecord(record)
				break
			}
		}
		if base.Version != 2 {
			return ErrState
		}
		approved := false
		for _, c := range state.Contacts {
			if c.Version == 2 && c.ContactID == base.ID && c.Local == base.Local && c.Peer == base.Peer {
				approved = true
			}
		}
		if !approved {
			return ErrState
		}
		a0, err := ContactBinding(base)
		if err != nil || a0 != r.Base {
			return ErrState
		}
		binding, err := routeBinding(base, r.Epoch, r.Active)
		if err != nil {
			return ErrState
		}
		if r.Epoch == 0 {
			if r.Agreement != nil || r.Last != nil {
				return ErrState
			}
		} else {
			if r.Agreement == nil || !approvalValid(base, r.Agreement.Proposal, r.Agreement.Approval) || r.Last == nil {
				return ErrState
			}
			proposal, err := proposalStatement(base, r.Agreement.Proposal)
			if err != nil || proposal.Epoch != r.Epoch || proposal.ServiceURL != r.Active.ServiceURL || proposal.RegionID != r.Active.RegionID {
				return ErrState
			}
			if r.Last.Agreement.Proposal == nil || r.Last.Agreement.Approval == nil || r.Last.Agreement.Proposal.signed().Digest() != r.Agreement.Proposal.signed().Digest() || r.Last.Agreement.Approval.signed().Digest() != r.Agreement.Approval.signed().Digest() || !commitValid(base, r.Agreement.Proposal, r.Last.LocalCommit, binding, role(base)) || !commitValid(base, r.Agreement.Proposal, r.Last.PeerCommit, binding, 3-role(base)) {
				return ErrState
			}
		}
		retired := map[string]bool{}
		for _, nonce := range r.Retired {
			if !pendingIDValid(nonce) || retired[nonce] {
				return ErrState
			}
			retired[nonce] = true
		}
		if p := r.Pending; p != nil {
			if !pendingIDValid(p.RequestID) || retired[p.RequestID] || ValidateRelayServiceURL(p.ServiceURL) != nil || r.Epoch == math.MaxUint64 {
				return ErrState
			}
			if p.Proposal == nil {
				if !p.LocalIntent || p.Approval != nil || p.Authenticated || p.LocalCommit != nil {
					return ErrState
				}
			} else {
				proposal, err := proposalStatement(base, p.Proposal)
				if err != nil || proposal.Nonce != p.RequestID || proposal.ServiceURL != p.ServiceURL || proposal.Epoch != r.Epoch+1 || proposal.Old != binding || p.LocalIntent != (proposal.Proposer == role(base)) {
					return ErrState
				}
				if p.Approval != nil && !approvalValid(base, p.Proposal, p.Approval) {
					return ErrState
				}
				both := approvalValid(base, p.Proposal, p.Approval)
				if (p.Authenticated || p.LocalCommit != nil) && !both {
					return ErrState
				}
				if p.LocalCommit != nil {
					nextBinding, err := routeBinding(base, proposal.Epoch, relayRoute{proposal.ServiceURL, proposal.RegionID})
					if err != nil || !p.Authenticated || !commitValid(base, p.Proposal, p.LocalCommit, nextBinding, role(base)) {
						return ErrState
					}
				}
			}
		}
		if r.Outgoing != nil {
			statement, err := r.Outgoing.signed().Verify(base.Local)
			if err != nil || statement.Signer != role(base) || statement.ContactID != base.ID || statement.Base != a0 || statement.Old != binding || statement.Epoch != r.Epoch+1 || !retired[statement.Nonce] || statement.Kind != relaychange.Decline && statement.Kind != relaychange.Cancel {
				return ErrState
			}
		}
	}
	return nil
}
