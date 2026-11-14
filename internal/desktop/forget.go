package desktop

import (
	"errors"

	"whatsup/internal/diagnostics"
	"whatsup/internal/messaging"
)

// ForgetContact is the explicitly confirmed, destructive contact action. The
// exact visible association ID prevents a delayed confirmation from removing a
// replacement contact. It removes local history/queued messages and contact
// credentials only; the device identity and OS wrapping credential are retained.
func (s *Service) ForgetContact(contactID string) (messaging.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.vault == nil || s.application == nil {
		return unavailableConversation(), errors.New("Unlock local storage before forgetting a contact")
	}
	if s.messages == nil {
		return s.failForget()
	}
	current := s.messages.Snapshot()
	if contactID == "" || current.ContactID != contactID {
		return current, errors.New("This contact is no longer selected. Review the current contact before forgetting it.")
	}
	if s.pairing == nil || s.invitations == nil {
		return s.failForget()
	}
	records, err := s.application.CommittedContacts()
	if err != nil || len(records) != 1 || records[0].ID != contactID {
		return current, errors.New("The contact could not be removed. Disconnect and unlock before retrying.")
	}
	err = s.application.ForgetCommittedContact(records[0], func() error {
		// Closing the old messaging manager first also makes a late pairing
		// completion callback unable to reconnect this retired generation.
		s.messages.Close()
		s.messages = nil
		s.invitations.close()
		s.invitations = nil
		err := s.pairing.Close()
		s.pairing = nil
		return err
	})
	if err != nil {
		return s.failForget()
	}
	local, err := s.application.Identity()
	if err != nil {
		return s.failForget()
	}
	messages, pairing, invitations, err := makeControllers(local, s.application)
	if err != nil {
		return s.failForget()
	}
	s.messages, s.pairing, s.invitations = messages, pairing, invitations
	return s.messages.Snapshot(), nil
}

// failForget keeps a failed/uncertain deletion disconnected. Closing storage
// invalidates every stale receipt and handle before the owner explicitly reopens
// the authenticated snapshot to determine whether the old association remains.
func (s *Service) failForget() (messaging.Snapshot, error) {
	if s.messages != nil {
		s.messages.Close()
		s.messages = nil
	}
	if s.invitations != nil {
		s.invitations.close()
		s.invitations = nil
	}
	if s.pairing != nil {
		_ = s.pairing.Close()
		s.pairing = nil
	}
	s.state = "unavailable"
	s.fingerprint = ""
	if s.vault != nil && s.vault.Close() == nil {
		s.vault = nil
		s.application = nil
		s.state = "locked"
	}
	s.record(diagnostics.StorageUnavailable)
	return unavailableConversation(), errors.New("Contact removal could not be completed. WhatsUp is disconnected. Unlock storage to review its saved state before retrying.")
}
