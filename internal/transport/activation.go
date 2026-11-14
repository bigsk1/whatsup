package transport

import (
	"encoding/base64"

	"github.com/tailscale/tailcat"
	"tailscale.com/types/key"

	"whatsup/internal/pairing"
	"whatsup/internal/pairing/channel"
	"whatsup/internal/storage"
)

// PromotePendingCredentials adopts exactly the keys agreed and saved by the
// authenticated exchange. It never calls the legacy random credential factory.
// Both one-shot completion and durable receipt are required; restored JSON and
// a locally approved Contact alone cannot create this private verified binding.
func PromotePendingCredentials(pending *PendingCredentials, contact *pairing.Contact, completion *channel.Completion, durable *storage.CommitReceipt) (*ContactCredentials, error) {
	if pending == nil || contact == nil || completion == nil || durable == nil || !contact.MatchesCompletion(completion) {
		return nil, ErrMutualApprovalUnavailable
	}
	record, err := pending.RecordForEncryptedStorage()
	if err != nil || durable.ValidateRecord(record) != nil || !record.Local.Equal(contact.Local()) || !record.Peer.Equal(contact.Peer()) {
		return nil, ErrInvalidCredentials
	}
	association, err := pairing.AssociationForRecord(record)
	if err != nil || !completion.MatchesAssociation(association) {
		return nil, ErrMutualApprovalUnavailable
	}
	var node key.NodePrivate
	var peer key.NodePublic
	if node.UnmarshalText([]byte(record.LocalNodePrivate)) != nil || peer.UnmarshalText([]byte(record.PeerNode)) != nil {
		return nil, ErrInvalidCredentials
	}
	psk, err := base64.RawURLEncoding.Strict().DecodeString(record.PresharedKey)
	defer clear(psk)
	if err != nil || len(psk) != 32 {
		return nil, ErrInvalidCredentials
	}
	state := &contactCredentialState{approval: contact, node: node}
	copy(state.psk[:], psk)
	state.binding = &verifiedBinding{localIdentity: record.Local, peerIdentity: record.Peer, localNode: node.Public(), peerNode: peer}
	if err := contact.PrepareTransportCredentials(func() error {
		return durable.ConsumeForActivation(record)
	}); err != nil {
		state.node, state.psk, state.binding = key.NodePrivate{}, tailcat.PresharedKey{}, nil
		return nil, ErrInvalidCredentials
	}
	pending.Revoke()
	return &ContactCredentials{state: state}, nil
}
