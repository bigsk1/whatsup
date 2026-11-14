package storage

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"whatsup/internal/identity"
	"whatsup/internal/pairing/channel"
)

// ContactBinding is the same versioned association on both roles. It excludes
// the local private key and binds every approved credential and routing fact.
// It is context for standard TLS authentication, not an invented authenticator.
func ContactBinding(record PendingRecord) ([32]byte, error) {
	if record.Version != 2 || ValidatePendingRecord(record) != nil {
		return [32]byte{}, ErrCommitted
	}
	route, err := channel.RoutingCommitment(record.RelayServiceURL, record.ServerEndpoint)
	if err != nil {
		return [32]byte{}, ErrCommitted
	}
	id, _ := base64.RawURLEncoding.DecodeString(record.ID)
	psk, _ := base64.RawURLEncoding.DecodeString(record.PresharedKey)
	defer clear(psk)
	pskHash := sha256.Sum256(psk)
	inviter, joiner := record.Local, record.Peer
	inviterNode, joinerNode := record.LocalNode, record.PeerNode
	if record.Role == "joiner" {
		inviter, joiner = record.Peer, record.Local
		inviterNode, joinerNode = record.PeerNode, record.LocalNode
	}
	server, _ := hex.DecodeString(inviterNode[len("nodekey:"):])
	client, _ := hex.DecodeString(joinerNode[len("nodekey:"):])
	wire := []byte("WhatsUp contact binding v1\x00")
	wire = append(wire, id...)
	wire = append(wire, inviter[:]...)
	wire = append(wire, joiner[:]...)
	wire = append(wire, server...)
	wire = append(wire, client...)
	wire = append(wire, pskHash[:]...)
	wire = append(wire, route[:]...)
	digest := sha256.Sum256(wire)
	clear(wire)
	return digest, nil
}

type identifiedContact interface {
	ApprovedContact
	ContactID() string
}

// ApproveCommittedConversation establishes durable LOCAL history/queue access
// after live pairing finalization or fresh mutual authentication. It grants no
// network capability. A new pairing cannot inherit a legacy/sibling chat scope.
func (a *ApplicationState) ApproveCommittedConversation(record PendingRecord, contact ApprovedContact) error {
	if a == nil || a.vault == nil || record.Version != 2 || ValidatePendingRecord(record) != nil || nilContact(contact) {
		return ErrCommitted
	}
	scoped, ok := contact.(identifiedContact)
	if !ok || scoped.ContactID() != record.ID || !contact.Local().Equal(record.Local) || !contact.Peer().Equal(record.Peer) {
		return ErrContact
	}
	return contact.WithAuthenticatedPeer(record.Peer, func() error {
		a.vault.mu.Lock()
		defer a.vault.mu.Unlock()
		if err := a.usableLocked(); err != nil {
			return err
		}
		if !a.hasCommittedLocked(record) {
			return ErrCommitted
		}
		if receipt := a.commitReceipts[record.ID]; receipt != nil && receipt.canceling {
			return ErrCommittedUsed
		}
		index := a.findContactLocked(record.Local, record.Peer)
		if index >= 0 {
			old := a.state.Contacts[index]
			if old.Version != 2 || old.ContactID != record.ID {
				return ErrCommittedConflict
			}
			return nil
		}
		if len(a.state.Contacts) >= MaxContacts {
			return ErrCapacity
		}
		next := a.cloneLocked()
		next.Contacts = append(next.Contacts, contactSection{Version: 2, Local: record.Local, Peer: record.Peer, ContactID: record.ID, StoreID: record.ID})
		return a.commitLocked(next)
	})
}

func (a *ApplicationState) hasCommittedLocked(record PendingRecord) bool {
	for _, current := range a.state.CommittedContacts {
		if current.ID == record.ID {
			return PendingRecord(current) == record
		}
	}
	return false
}

// LocalContactReceipt proves a durable local conversation approval in this open
// vault. Unlike a live TLS proof, it permits only offline history and queuing.
// It cannot be serialized or used as a transport/session credential.
type LocalContactReceipt struct{ state *localContactReceiptState }
type localContactReceiptState struct {
	application *ApplicationState
	record      PendingRecord
}

func (LocalContactReceipt) String() string               { return "[private WhatsUp local conversation receipt]" }
func (LocalContactReceipt) GoString() string             { return "[private WhatsUp local conversation receipt]" }
func (LocalContactReceipt) MarshalJSON() ([]byte, error) { return nil, ErrCommitted }
func (r *LocalContactReceipt) Local() identity.PublicKey {
	if r == nil || r.state == nil {
		return identity.PublicKey{}
	}
	return r.state.record.Local
}
func (r *LocalContactReceipt) Peer() identity.PublicKey {
	if r == nil || r.state == nil {
		return identity.PublicKey{}
	}
	return r.state.record.Peer
}
func (r *LocalContactReceipt) ContactID() string {
	if r == nil || r.state == nil {
		return ""
	}
	return r.state.record.ID
}

// LocalConversation can restore LOCAL approval while the peer is offline, but
// only for a v2 committed contact whose exact conversation pin was durably saved.
// A merely pending/committed setup without that pin still grants no history.
func (a *ApplicationState) LocalConversation(record PendingRecord) (*LocalContactReceipt, error) {
	if a == nil || a.vault == nil || record.Version != 2 || ValidatePendingRecord(record) != nil {
		return nil, ErrCommitted
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.localConversationLocked(record); err != nil {
		return nil, err
	}
	if a.localReceipts == nil {
		a.localReceipts = make(map[string]*localContactReceiptState)
	}
	state := a.localReceipts[record.ID]
	if state == nil {
		state = &localContactReceiptState{application: a, record: record}
		a.localReceipts[record.ID] = state
	}
	if state.record != record {
		return nil, ErrCommittedConflict
	}
	return &LocalContactReceipt{state: state}, nil
}

func (a *ApplicationState) localConversationLocked(record PendingRecord) error {
	if err := a.usableLocked(); err != nil {
		return err
	}
	if !a.hasCommittedLocked(record) {
		return ErrCommitted
	}
	if receipt := a.commitReceipts[record.ID]; receipt != nil && receipt.canceling {
		return ErrCommittedUsed
	}
	index := a.findContactLocked(record.Local, record.Peer)
	if index < 0 || a.state.Contacts[index].Version != 2 || a.state.Contacts[index].ContactID != record.ID {
		return ErrContact
	}
	return nil
}

// WithLocalApproval guards one bounded, non-IO constructor. The callback must
// not reenter storage or perform IO. Runtime contacts retain their own revoke
// guard; they must NOT wrap disk-writing operations in this vault guard.
func (r *LocalContactReceipt) WithLocalApproval(operation func() error) error {
	if r == nil || r.state == nil || r.state.application == nil || operation == nil {
		return ErrContact
	}
	state := r.state
	a := state.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if a.localReceipts[state.record.ID] != state {
		return ErrContact
	}
	if err := a.localConversationLocked(state.record); err != nil {
		return err
	}
	return operation()
}

func (a *ApplicationState) removeConversationLocked(next *applicationEnvelope, id string) {
	for index, contact := range next.Contacts {
		if contact.Version == 2 && contact.ContactID == id {
			next.DeleteScopes = append(next.DeleteScopes, contact.StoreID)
			copy(next.Contacts[index:], next.Contacts[index+1:])
			clear(next.Contacts[len(next.Contacts)-1:])
			next.Contacts = next.Contacts[:len(next.Contacts)-1]
			break
		}
	}
}
