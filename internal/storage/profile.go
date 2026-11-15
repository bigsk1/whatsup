package storage

import (
	"math"

	"whatsup/internal/identity"
	"whatsup/internal/profile"
)

// Profile returns only the owner's optional display label and sharing policy.
// No read or write in this file starts network work or changes identity authority.
func (a *ApplicationState) Profile() (profile.Local, error) {
	if a == nil || a.vault == nil {
		return profile.Local{}, ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return profile.Local{}, err
	}
	return a.state.Profile, nil
}

// SetProfile persists an explicit owner choice. An unchanged save has no disk
// effect or revision change. Turning sharing off retains the owner's local name.
func (a *ApplicationState) SetProfile(name string, share bool) (profile.Local, error) {
	if a == nil || a.vault == nil {
		return profile.Local{}, ErrLocked
	}
	if err := profile.ValidateName(name); err != nil {
		return profile.Local{}, err
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return profile.Local{}, err
	}
	current := a.state.Profile
	if current.DisplayName == name && current.ShareDisplayName == share {
		return current, nil
	}
	if current.Revision == math.MaxUint64 {
		return profile.Local{}, ErrCapacity
	}
	next := a.cloneLocked()
	next.Profile.DisplayName, next.Profile.ShareDisplayName, next.Profile.Revision = name, share, current.Revision+1
	if err := a.commitLocked(next); err != nil {
		return profile.Local{}, err
	}
	return next.Profile, nil
}

// ContactNames reads names only for an existing, durably approved association.
func (a *ApplicationState) ContactNames(id string) (profile.Contact, error) {
	if a == nil || a.vault == nil {
		return profile.Contact{}, ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	index, _, err := a.nameContactLocked(id)
	if err != nil {
		return profile.Contact{}, err
	}
	contact := a.state.Contacts[index]
	return profile.Contact{LocalName: contact.LocalName, PeerDisplayName: contact.PeerName}, nil
}

// SetContactName saves a local-only label. It never alters a received peer name
// or the current chat generation, so an open scoped chat writer remains valid.
func (a *ApplicationState) SetContactName(id, name string) error {
	if a == nil || a.vault == nil {
		return ErrLocked
	}
	if err := profile.ValidateName(name); err != nil {
		return err
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	index, _, err := a.nameContactLocked(id)
	if err != nil {
		return err
	}
	if a.state.Contacts[index].LocalName == name {
		return nil
	}
	next := a.cloneLocked()
	next.Contacts[index].LocalName = name
	return a.commitLocked(next)
}

// SetPeerDisplayName is backend-only. Its caller must have authenticated this
// exact peer on the ongoing contact session; names themselves grant no trust.
// A missing, replaced or claimed-for-cancellation record rejects the write.
func (a *ApplicationState) SetPeerDisplayName(id string, peer identity.PublicKey, name string) error {
	if a == nil || a.vault == nil {
		return ErrLocked
	}
	if err := profile.ValidateName(name); err != nil {
		return err
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	index, record, err := a.nameContactLocked(id)
	if err != nil {
		return err
	}
	if !record.Peer.Equal(peer) {
		return ErrContact
	}
	if a.state.Contacts[index].PeerName == name {
		return nil
	}
	next := a.cloneLocked()
	next.Contacts[index].PeerName = name
	return a.commitLocked(next)
}

// nameContactLocked requires the same exact durable local conversation approval
// as history access. The caller holds the vault guard throughout its operation.
func (a *ApplicationState) nameContactLocked(id string) (int, PendingRecord, error) {
	if err := a.usableLocked(); err != nil {
		return -1, PendingRecord{}, err
	}
	if !pendingIDValid(id) {
		return -1, PendingRecord{}, ErrContact
	}
	for _, disk := range a.state.CommittedContacts {
		record := PendingRecord(disk)
		if record.Version != 2 || record.ID != id {
			continue
		}
		if err := a.localConversationLocked(record); err != nil {
			return -1, PendingRecord{}, err
		}
		return a.findContactLocked(record.Local, record.Peer), record, nil
	}
	return -1, PendingRecord{}, ErrContact
}
