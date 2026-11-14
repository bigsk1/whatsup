package pairing

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"whatsup/internal/identity"
	"whatsup/internal/pairing/channel"
	"whatsup/internal/storage"
	"whatsup/internal/transport/address"
)

// AssociationForRecord normalizes the exact agreed keys, not a replacement
// credential set. Completion also compares this with its private TLS transcript.
func AssociationForRecord(record storage.PendingRecord) (channel.ActivationAssociation, error) {
	var result channel.ActivationAssociation
	if storage.ValidatePendingRecord(record) != nil {
		return result, ErrActivation
	}
	id, _ := base64.RawURLEncoding.DecodeString(record.ID)
	local, _ := hex.DecodeString(record.LocalNode[len("nodekey:"):])
	peer, _ := hex.DecodeString(record.PeerNode[len("nodekey:"):])
	psk, _ := base64.RawURLEncoding.DecodeString(record.PresharedKey)
	defer clear(psk)
	copy(result.ID[:], id)
	result.PSKCommitment = sha256.Sum256(psk)
	routing, err := channel.RoutingCommitment(record.RelayServiceURL, record.ServerEndpoint)
	if err != nil {
		return channel.ActivationAssociation{}, ErrActivation
	}
	result.RoutingCommitment = routing
	if record.Role == "inviter" {
		result.InviterIdentity, result.JoinerIdentity = record.Local, record.Peer
		copy(result.InviterNode[:], local)
		copy(result.JoinerNode[:], peer)
	} else {
		result.InviterIdentity, result.JoinerIdentity = record.Peer, record.Local
		copy(result.InviterNode[:], peer)
		copy(result.JoinerNode[:], local)
	}
	return result, nil
}

// FinalizeAuthenticated detaches one new contact from a live, one-shot mutual
// completion and the exact durable record. The inviter must supply its reserved
// Manager; the joiner supplies nil. No network IO runs under either guard.
// Persisted records alone cannot recreate either capability after restart.
func FinalizeAuthenticated(manager *Manager, completion *channel.Completion, durable *storage.CommitReceipt) (*Contact, error) {
	if completion == nil || durable == nil {
		return nil, ErrActivation
	}
	record, err := durable.RecordForActivation()
	if err != nil {
		return nil, ErrActivation
	}
	association, err := AssociationForRecord(record)
	if err != nil || !completion.MatchesAssociation(association) || !completion.Local().Equal(record.Local) || !completion.Peer().Equal(record.Peer) {
		return nil, ErrActivation
	}
	if (record.Role == "inviter" && (completion.Role() != channel.Inviter || manager == nil)) || (record.Role == "joiner" && (completion.Role() != channel.Joiner || manager != nil)) {
		return nil, ErrActivation
	}
	if manager != nil {
		manager.mu.Lock()
		manager.expireLocked()
		valid := !manager.closed && manager.state == Pending && !manager.completionClaimed && manager.local.Equal(record.Local) && manager.candidate.Equal(record.Peer) && completion.MatchesSession(manager.authenticatedChannel)
		if valid {
			manager.completionClaimed = true
		}
		manager.mu.Unlock()
		if !valid {
			return nil, ErrActivation
		}
	}
	var contact *Contact
	// Do not acquire Manager.mu here: its expiry retirement hook can close this
	// channel. The durable guard does no IO and cannot acquire a manager lock.
	err = completion.ConsumeLiveOperation(func() error {
		if durable.ValidateRecord(record) != nil {
			return ErrActivation
		}
		var createErr error
		contact, createErr = newContact(record.Local, record.Peer)
		if createErr == nil {
			contact.state.completion = completion
			contact.state.contactID = record.ID
		}
		return createErr
	})
	if err != nil {
		return nil, ErrActivation
	}
	if manager == nil {
		return contact, nil
	}
	manager.mu.Lock()
	manager.expireLocked()
	if manager.closed || manager.state != Pending || !manager.now().Before(manager.invite.expires) {
		manager.mu.Unlock()
		contact.Revoke()
		return nil, ErrActivation
	}
	expires := manager.invite.expires
	if manager.timer != nil {
		manager.timer.Stop()
		manager.timer = nil
	}
	manager.state = Consumed
	clear(manager.invite.secret[:])
	manager.invite.endpoint = address.Endpoint{}
	manager.candidate = identity.PublicKey{}
	manager.authenticatedChannel = nil
	done := make(chan struct{})
	manager.completionDone = done
	retire := manager.close
	manager.mu.Unlock()
	var retireErr error
	if retire != nil {
		retireErr = retire()
	}
	manager.mu.Lock()
	if retireErr != nil || !manager.now().Before(expires) || manager.closed {
		manager.state, manager.closeErr = Revoked, ErrListenerClose
		contact.Revoke()
	}
	manager.completionDone = nil
	close(done)
	failed := manager.closeErr != nil
	manager.mu.Unlock()
	if failed {
		return nil, ErrListenerClose
	}
	return contact, nil
}
