package desktop

import (
	"errors"

	"whatsup/internal/messaging"
)

// ProfileSnapshot contains only explicitly editable public profile metadata.
// Names remain in encrypted Go storage, never frontend preferences or logs.
type ProfileSnapshot struct {
	DisplayName      string `json:"displayName"`
	ShareDisplayName bool   `json:"shareDisplayName"`
}

func (s *Service) GetProfile() (ProfileSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.application == nil {
		return ProfileSnapshot{}, errors.New("Unlock local storage to view your profile")
	}
	local, err := s.application.Profile()
	if err != nil {
		return ProfileSnapshot{}, errors.New("Your saved profile could not be verified. Disconnect and unlock before retrying.")
	}
	return ProfileSnapshot{DisplayName: local.DisplayName, ShareDisplayName: local.ShareDisplayName}, nil
}

func (s *Service) SetProfile(displayName string, shareDisplayName bool) (ProfileSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.application == nil || s.messages == nil {
		return ProfileSnapshot{}, errors.New("Unlock local storage before saving your profile")
	}
	local, err := s.application.SetProfile(displayName, shareDisplayName)
	if err != nil {
		return ProfileSnapshot{}, errors.New("Your profile could not be saved. Use a name within the supported limit or disconnect and unlock before retrying.")
	}
	s.messages.MetadataChanged()
	return ProfileSnapshot{DisplayName: local.DisplayName, ShareDisplayName: local.ShareDisplayName}, nil
}

func (s *Service) SetContactName(contactID, localName string) (messaging.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.application == nil || s.messages == nil {
		return unavailableConversation(), errors.New("Unlock local storage before naming this contact")
	}
	current := s.messages.Snapshot()
	if contactID == "" || current.ContactID != contactID {
		return current, errors.New("This contact is no longer selected. Review the current contact before naming it.")
	}
	if err := s.application.SetContactName(contactID, localName); err != nil {
		return current, errors.New("The contact name could not be saved. Use a name within the supported limit or disconnect and unlock before retrying.")
	}
	s.messages.MetadataChanged()
	return s.messages.Snapshot(), nil
}
