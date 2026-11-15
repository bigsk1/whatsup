package desktop

import "errors"

// ContactStorage exposes only logical quota counters for an approved contact.
// Reservations/tombstones count; this is neither OS free space nor exact disk use.
type ContactStorage struct {
	ContactID             string `json:"contactID"`
	MessagesUsedBytes     uint64 `json:"messagesUsedBytes"`
	MessagesQuotaBytes    uint64 `json:"messagesQuotaBytes"`
	AttachmentsUsedBytes  uint64 `json:"attachmentsUsedBytes"`
	AttachmentsQuotaBytes uint64 `json:"attachmentsQuotaBytes"`
}

func (s *Service) GetContactStorage(contactID string) (ContactStorage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.application == nil || s.messages == nil {
		return ContactStorage{}, errors.New("Unlock local storage before viewing contact storage")
	}
	usage, err := s.application.ContactStorageUsage(contactID)
	if err != nil {
		return ContactStorage{}, errors.New("This contact's local storage usage could not be verified")
	}
	return ContactStorage{ContactID: contactID, MessagesUsedBytes: usage.Messages.UsedBytes, MessagesQuotaBytes: usage.Messages.QuotaBytes, AttachmentsUsedBytes: usage.Attachments.UsedBytes, AttachmentsQuotaBytes: usage.Attachments.QuotaBytes}, nil
}
