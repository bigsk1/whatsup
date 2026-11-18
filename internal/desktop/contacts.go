package desktop

import (
	"errors"

	"whatsup/internal/messaging"
	"whatsup/internal/storage"
)

func (s *Service) GetContacts() ([]messaging.ContactSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return []messaging.ContactSummary{}, nil
	}
	contacts, err := s.messages.Contacts()
	if err != nil {
		return nil, errors.New("Saved contacts could not be verified")
	}
	return contacts, nil
}

// SetContactPaused changes only the selected approved contact's connection.
func (s *Service) SetContactPaused(contactID string, paused bool) (messaging.ContactSummary, error) {
	s.mu.Lock()
	defer s.finishStorageCall(s.state == "unlocked")
	if s.state != "unlocked" || s.messages == nil {
		return messaging.ContactSummary{}, errors.New("Unlock local storage before changing this connection")
	}
	contact, err := s.messages.SetPaused(contactID, paused)
	if errors.Is(err, storage.ErrStateFailed) {
		s.failStorageWriteLocked()
		return messaging.ContactSummary{}, errors.New("The connection setting could not be saved. Unlock local storage before retrying")
	}
	if err != nil {
		return contact, errors.New("This contact's connection could not be changed. Check that the contact is still available")
	}
	if paused && s.attachments != nil && s.attachments.session != nil {
		// Cancel any picker or staging operation for this contact. Keep draft
		// handles, saved files, previews, and the other contacts' leases.
		g := s.attachments.session
		if current := g.contacts[contactID]; current != nil {
			current.cancel()
			delete(g.contacts, contactID)
		}
	}
	return contact, nil
}

func (s *Service) GetMessages(contactID, cursor string, limit int) (messaging.MessagePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return messaging.MessagePage{}, errors.New("Unlock local storage before reading messages")
	}
	page, err := s.messages.Page(contactID, cursor, limit)
	if err != nil {
		return messaging.MessagePage{}, errors.New("This message page could not be verified")
	}
	return page, nil
}

func (s *Service) MarkRead(contactID, throughMessageID string) (messaging.ContactSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return messaging.ContactSummary{}, errors.New("Unlock local storage before marking messages read")
	}
	contact, err := s.messages.MarkRead(contactID, throughMessageID)
	if err != nil {
		return messaging.ContactSummary{}, errors.New("The read position could not be saved")
	}
	return contact, nil
}
