package desktop

import (
	"errors"

	"whatsup/internal/messaging"
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
