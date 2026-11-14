package storage

import "whatsup/internal/messagestore"

// peerRemovedLocked is for contact routing coordinators that already hold the
// vault guard. It must not call the public getter and acquire that guard twice.
func (a *ApplicationState) peerRemovedLocked(id string) (bool, error) {
	if err := a.usableLocked(); err != nil {
		return false, err
	}
	for _, disk := range a.state.CommittedContacts {
		if disk.ID != id {
			continue
		}
		scope, err := a.peerRemovalScopeLocked(PendingRecord(disk))
		if err != nil {
			return false, err
		}
		marker, err := scope.PeerRemoval()
		return marker != "", err
	}
	return false, ErrContact
}

// These Go-only methods operate on one exact durable association. A caller of
// MarkPeerRemoved must first authenticate the notice on that association's live
// session; receipt acknowledgement must follow this durable transaction.
func (a *ApplicationState) peerRemovalScopeLocked(record PendingRecord) (*messagestore.Scope, error) {
	if err := a.localConversationLocked(record); err != nil {
		return nil, err
	}
	index := a.findContactLocked(record.Local, record.Peer)
	if index < 0 || a.messages == nil {
		return nil, ErrContact
	}
	return a.messages.Scope(a.state.Contacts[index].StoreID)
}

func (a *ApplicationState) PeerRemoval(record PendingRecord) (string, error) {
	if a == nil || a.vault == nil {
		return "", ErrContact
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	scope, err := a.peerRemovalScopeLocked(record)
	if err != nil {
		return "", err
	}
	return scope.PeerRemoval()
}

func (a *ApplicationState) MarkPeerRemoved(record PendingRecord, operationID string) error {
	if a == nil || a.vault == nil || !pendingIDValid(operationID) {
		return ErrContact
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	scope, err := a.peerRemovalScopeLocked(record)
	if err != nil {
		return err
	}
	return scope.MarkPeerRemoved(operationID)
}
