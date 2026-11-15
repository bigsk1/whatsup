package storage

import (
	"errors"
	"whatsup/internal/attachmentstore"
	"whatsup/internal/messagestore"
)

// ContactStorageUsage is Go-only read authority for one exact saved local
// conversation. Pending/committed pairing alone grants no quota/history access.
func (a *ApplicationState) ContactStorageUsage(contactID string) (messagestore.ContactUsage, error) {
	if a == nil || a.vault == nil {
		return messagestore.ContactUsage{}, ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	index, _, err := a.nameContactLocked(contactID)
	if err != nil {
		return messagestore.ContactUsage{}, err
	}
	if index < 0 || a.messages == nil || a.attachments == nil || a.state.MessageStoreState != "ready" {
		return messagestore.ContactUsage{}, ErrState
	}
	scope, err := a.messages.ExistingScope(a.state.Contacts[index].StoreID)
	if err != nil {
		return messagestore.ContactUsage{}, err
	}
	if scope == nil {
		// A newly approved contact can have no database rows yet. A read does
		// not manufacture a contact scope or mutate the encrypted manifest.
		return messagestore.ContactUsage{Messages: messagestore.Usage{QuotaBytes: messagestore.DefaultQuotaBytes}, Attachments: attachmentstore.Usage{QuotaBytes: attachmentstore.QuotaBytes}}, nil
	}
	usage, err := scope.StorageUsage()
	if errors.Is(err, messagestore.ErrStore) || errors.Is(err, messagestore.ErrCorrupt) {
		a.failed = true
	}
	return usage, err
}
