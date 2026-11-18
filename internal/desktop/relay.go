package desktop

import (
	"errors"

	"whatsup/internal/storage"
)

type RelayChangeSnapshot = storage.RelayChangeSnapshot

type RelayCarry struct {
	Text string `json:"text"`
}

var errRelayChange = errors.New("The connection service change could not be completed. Review this contact's current connection service before trying again.")

func (s *Service) relayFailureLocked(err error) error {
	if errors.Is(err, storage.ErrStateFailed) {
		s.failStorageWriteLocked()
	}
	return errRelayChange
}

func (s *Service) relaySnapshotLocked(id string) (RelayChangeSnapshot, error) {
	if s.state != "unlocked" || s.application == nil || s.messages == nil {
		return RelayChangeSnapshot{}, errRelayChange
	}
	value, err := s.application.RelayChange(id)
	if err != nil {
		return RelayChangeSnapshot{}, s.relayFailureLocked(err)
	}
	contact := s.messages.Snapshot(id)
	if contact.ContactID != id {
		return RelayChangeSnapshot{}, errRelayChange
	}
	// The public revision uses the same Service-lifetime clock as contact hints.
	// The encrypted ledger's own revision remains a storage implementation fact.
	value.Revision = contact.Revision
	return value, nil
}

// GetRelayChange performs only local authenticated inspection. It never fetches
// a map, dials, selects a provider, or approves a pending peer proposal.
func (s *Service) GetRelayChange(id string) (RelayChangeSnapshot, error) {
	s.mu.Lock()
	defer s.finishStorageCall(s.state == "unlocked")
	return s.relaySnapshotLocked(id)
}

func (s *Service) relayChangedLocked(id string) (RelayChangeSnapshot, error) {
	if err := s.messages.RelayChanged(id); err != nil {
		return RelayChangeSnapshot{}, s.relayFailureLocked(err)
	}
	return s.relaySnapshotLocked(id)
}

func (s *Service) ProposeRelayChange(id, serviceURL string, consent bool) (RelayChangeSnapshot, error) {
	s.mu.Lock()
	defer s.finishStorageCall(s.state == "unlocked")
	if !consent {
		return RelayChangeSnapshot{}, errors.New("Allow this connection service before proposing the change")
	}
	if _, err := s.relaySnapshotLocked(id); err != nil {
		return RelayChangeSnapshot{}, err
	}
	if _, err := s.application.BeginRelayChange(id, serviceURL); err != nil {
		return RelayChangeSnapshot{}, s.relayFailureLocked(err)
	}
	return s.relayChangedLocked(id)
}

func (s *Service) ApproveRelayChange(id, requestID string, consent bool) (RelayChangeSnapshot, error) {
	s.mu.Lock()
	defer s.finishStorageCall(s.state == "unlocked")
	if !consent {
		return RelayChangeSnapshot{}, errors.New("Allow the displayed connection service before approving the change")
	}
	if _, err := s.relaySnapshotLocked(id); err != nil {
		return RelayChangeSnapshot{}, err
	}
	if _, err := s.application.ApproveRelayChange(id, requestID); err != nil {
		return RelayChangeSnapshot{}, s.relayFailureLocked(err)
	}
	return s.relayChangedLocked(id)
}

func (s *Service) DeclineRelayChange(id, requestID string) (RelayChangeSnapshot, error) {
	s.mu.Lock()
	defer s.finishStorageCall(s.state == "unlocked")
	if _, err := s.relaySnapshotLocked(id); err != nil {
		return RelayChangeSnapshot{}, err
	}
	if _, err := s.application.DeclineRelayChange(id, requestID); err != nil {
		return RelayChangeSnapshot{}, s.relayFailureLocked(err)
	}
	return s.relayChangedLocked(id)
}

func (s *Service) CancelRelayChange(id, requestID string) (RelayChangeSnapshot, error) {
	s.mu.Lock()
	defer s.finishStorageCall(s.state == "unlocked")
	if _, err := s.relaySnapshotLocked(id); err != nil {
		return RelayChangeSnapshot{}, err
	}
	if _, err := s.application.CancelRelayChange(id, requestID); err != nil {
		return RelayChangeSnapshot{}, s.relayFailureLocked(err)
	}
	return s.relayChangedLocked(id)
}

// ExportRelayChange returns a signed, bounded contribution. It contains no
// endpoint, node private key, PSK or session capability; copying stays explicit.
func (s *Service) ExportRelayChange(id string) (RelayCarry, error) {
	s.mu.Lock()
	defer s.finishStorageCall(s.state == "unlocked")
	if _, err := s.relaySnapshotLocked(id); err != nil {
		return RelayCarry{}, err
	}
	text, err := s.application.ExportRelayChange(id)
	if err != nil {
		return RelayCarry{}, s.relayFailureLocked(err)
	}
	return RelayCarry{Text: text}, nil
}

// ImportRelayChange verifies the exact saved peer's contribution locally.
// Importing a proposal never manufactures local approval or permits its fetch.
func (s *Service) ImportRelayChange(id, text string) (RelayChangeSnapshot, error) {
	s.mu.Lock()
	defer s.finishStorageCall(s.state == "unlocked")
	if _, err := s.relaySnapshotLocked(id); err != nil {
		return RelayChangeSnapshot{}, err
	}
	if _, err := s.application.ImportRelayChange(id, text); err != nil {
		return RelayChangeSnapshot{}, s.relayFailureLocked(err)
	}
	return s.relayChangedLocked(id)
}
