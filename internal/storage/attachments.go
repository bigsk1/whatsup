package storage

import "whatsup/internal/attachmentstore"

func (a *ApplicationState) openAttachmentStoreLocked() error {
	store, err := attachmentstore.Open(a.vault.directory)
	if err != nil {
		return err
	}
	a.attachments = store
	return nil
}
func (a *ApplicationState) closeAttachmentStoreLocked() error {
	if a.attachments == nil {
		return nil
	}
	store := a.attachments
	a.attachments = nil
	return store.Close()
}

// AttachmentStore is a Go-only capability for one exact committed, approved
// association. Pending pairings never receive a repository or object key.
func (a *ApplicationState) AttachmentStore(contactID string) (*attachmentstore.Scope, error) {
	if a == nil || a.vault == nil {
		return nil, ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return nil, err
	}
	if a.messages == nil || a.attachments == nil {
		return nil, ErrContact
	}
	for _, contact := range a.state.Contacts {
		if contact.Version != 2 || contact.ContactID != contactID {
			continue
		}
		for _, record := range a.state.CommittedContacts {
			if record.Version != 2 || record.ID != contactID || record.Local != contact.Local || record.Peer != contact.Peer {
				continue
			}
			if state := a.commitReceipts[contactID]; state != nil && state.canceling {
				return nil, ErrContact
			}
			scope, err := a.messages.Scope(contact.StoreID)
			if err != nil {
				return nil, err
			}
			return a.attachments.Scope(contact.StoreID, scope.Attachments())
		}
	}
	return nil, ErrContact
}
