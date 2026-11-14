package desktop

import (
	"errors"
	"whatsup/internal/messaging"
)

func (s *Service) ClearHistory(contactID string) (messaging.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return unavailableConversation(), errors.New("Unlock local storage before clearing history")
	}
	snapshot, err := s.messages.ClearHistory(contactID)
	if err != nil {
		return snapshot, errors.New("Local history could not be cleared")
	}
	return snapshot, nil
}
func (s *Service) DeleteMessage(contactID, messageID string) (messaging.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return unavailableConversation(), errors.New("Unlock local storage before deleting a local message")
	}
	snapshot, err := s.messages.DeleteMessage(contactID, messageID)
	if err != nil {
		return snapshot, errors.New("The local message could not be deleted")
	}
	return snapshot, nil
}
