package desktop

import (
	"context"
	"errors"
	"time"

	"whatsup/internal/diagnostics"
	"whatsup/internal/messaging"
)

// ForgetContact is the explicitly confirmed, destructive contact action. The
// exact visible association ID prevents a delayed confirmation from removing a
// replacement contact. It removes local history/queued messages and contact
// credentials only; the device identity and OS wrapping credential are retained.
func (s *Service) ForgetContact(contactID string) (messaging.Snapshot, error) {
	s.mu.Lock()
	forgotten := false
	defer func() {
		onForgotten := s.runtimeOptions.OnContactForgotten
		s.mu.Unlock()
		if forgotten && onForgotten != nil {
			onForgotten(contactID)
		}
	}()
	if s.state != "unlocked" || s.vault == nil || s.application == nil {
		return unavailableConversation(), errors.New("Unlock local storage before forgetting a contact")
	}
	if s.messages == nil {
		return s.failForget()
	}
	current := s.messages.Snapshot(contactID)
	if contactID == "" || current.ContactID != contactID {
		return s.firstConversationLocked(), errors.New("This contact is no longer available. Review the current contact before forgetting it.")
	}
	if s.pairing == nil || s.invitations == nil {
		return s.failForget()
	}
	records, err := s.application.CommittedContacts()
	if err != nil {
		return current, errors.New("The contact could not be removed. Disconnect and unlock before retrying.")
	}
	index := -1
	for i := range records {
		if records[i].ID == contactID {
			index = i
			break
		}
	}
	if index < 0 {
		return current, errors.New("This contact is no longer available")
	}
	// Notify only an already authenticated online session. The bounded result
	// never gates local deletion and cannot fetch a map or create a connection.
	notice, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, _ = s.messages.NotifyRemoved(notice, contactID)
	cancel()
	s.retireAttachmentsLocked(contactID)
	err = s.application.ForgetCommittedContact(records[index], func() error {
		// Retire sets a permanent generation tombstone before joining this exact
		// runtime. A late pairing callback cannot recreate it during deletion.
		return s.messages.Retire(contactID)
	})
	if err != nil {
		return s.failForget()
	}
	forgotten = true
	return s.firstConversationLocked(), nil
}

// failForget keeps a failed/uncertain deletion disconnected. Closing storage
// invalidates every stale receipt and handle before the owner explicitly reopens
// the authenticated snapshot to determine whether the old association remains.
func (s *Service) failForget() (messaging.Snapshot, error) {
	s.stopAttachmentsLocked()
	if s.messages != nil {
		s.messages.Close()
		s.messages = nil
		s.resetCalls()
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
