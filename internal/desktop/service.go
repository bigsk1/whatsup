// Package desktop is the complete, deliberately narrow Wails application bridge.
// Identity, pairing, chat, transport and vault objects are never bound directly.
package desktop

import (
	"errors"
	"sync"

	"whatsup/internal/diagnostics"
	"whatsup/internal/pairflow"
	"whatsup/internal/storage"
)

const Version = "0.0.3"

type Status struct {
	Version              string `json:"version"`
	IdentityFingerprint  string `json:"identityFingerprint"`
	StorageState         string `json:"storageState"`
	NetworkState         string `json:"networkState"`
	DirectOnlyAvailable  bool   `json:"directOnlyAvailable"`
	PairingAvailable     bool   `json:"pairingAvailable"`
	MessagingAvailable   bool   `json:"messagingAvailable"`
	AttachmentsAvailable bool   `json:"attachmentsAvailable"`
}

type Service struct {
	mu          sync.Mutex
	directory   string
	keys        storage.KeyStore
	vault       *storage.Vault
	application *storage.ApplicationState
	pairing     *pairflow.Coordinator
	invitations *invitationControl
	fingerprint string
	state       string
	record      func(diagnostics.Event)
}

func New(directory string, keys storage.KeyStore, listeners ...func(diagnostics.Event)) *Service {
	record := func(diagnostics.Event) {}
	if len(listeners) != 0 && listeners[0] != nil {
		record = listeners[0]
	}
	return &Service{directory: directory, keys: keys, state: "locked", record: record}
}

func (s *Service) status() Status {
	return Status{Version: Version, IdentityFingerprint: s.fingerprint, StorageState: s.state, NetworkState: "unavailable", PairingAvailable: s.state == "unlocked" && s.invitations != nil}
}

func (s *Service) GetStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status()
}

// UnlockStorage opens OS-protected local identity state. It starts no listener,
// emits no invitation, and does not claim an available messaging capability.
func (s *Service) UnlockStorage() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.vault != nil {
		if s.state == "unlocked" {
			return s.status(), nil
		}
		if err := s.vault.Close(); err != nil {
			s.record(diagnostics.StorageUnavailable)
			return s.status(), errors.New("Local storage lock could not be released. Quit WhatsUp and try again.")
		}
		s.vault = nil
		s.application = nil
	}
	vault, err := storage.Open(s.directory, s.keys)
	if err != nil {
		s.state = "unavailable"
		s.record(diagnostics.StorageUnavailable)
		return s.status(), errors.New("Local storage could not be unlocked. Check OS credential protection and whether WhatsUp is already running.")
	}
	fail := func() (Status, error) {
		if err := vault.Close(); err != nil {
			s.vault = vault
		}
		s.state = "unavailable"
		s.record(diagnostics.StorageUnavailable)
		return s.status(), errors.New("Local state could not be authenticated. Preserve the existing state and its OS credential; no replacement was created.")
	}
	application, err := storage.OpenApplication(vault)
	if err != nil {
		return fail()
	}
	local, err := application.Identity()
	if err != nil {
		return fail()
	}
	pairing, err := pairflow.New(local, application, pairflow.Options{CompleteActivation: true})
	if err != nil {
		return fail()
	}
	s.fingerprint = local.PublicKey().Fingerprint()
	s.vault = vault
	s.application = application
	s.pairing = pairing
	s.invitations = newInvitationControl(local, pairing)
	s.state = "unlocked"
	s.record(diagnostics.StorageUnlocked)
	return s.status(), nil
}

func (s *Service) LockStorage() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.invitations != nil {
		s.invitations.close()
		s.invitations = nil
	}
	if s.pairing != nil {
		// Join every pairing/persistence callback before closing the vault. This
		// also closes its owned TLS session and retires any invitation transport.
		if s.pairing.Close() != nil {
			s.record(diagnostics.RuntimeError)
		}
		s.pairing = nil
	}
	if s.vault != nil {
		if err := s.vault.Close(); err != nil {
			s.fingerprint = ""
			s.state = "unavailable"
			s.record(diagnostics.StorageUnavailable)
			return s.status()
		}
		s.vault = nil
	}
	s.application = nil
	s.fingerprint = ""
	s.state = "locked"
	s.record(diagnostics.StorageLocked)
	return s.status()
}

func (s *Service) GetPairingStatus() pairflow.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.pairing == nil {
		return pairflow.Snapshot{State: "unavailable"}
	}
	if s.invitations != nil {
		return s.invitations.snapshot()
	}
	return s.pairing.Snapshot()
}

// ApprovePairing confirms only the current reserved identity. It neither opens
// a listener nor returns credentials, contact authority or history access.
func (s *Service) ApprovePairing(requestID, peerFingerprint string) (pairflow.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.pairing == nil {
		return pairflow.Snapshot{State: "unavailable"}, errors.New("Unlock local storage before reviewing pairing")
	}
	status, err := s.pairing.Approve(requestID, peerFingerprint)
	if err != nil {
		return status, errors.New("This pairing request is no longer available")
	}
	return status, nil
}

func (s *Service) DenyPairing(requestID string) (pairflow.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.pairing == nil {
		return pairflow.Snapshot{State: "unavailable"}, errors.New("Unlock local storage before reviewing pairing")
	}
	status, err := s.pairing.Deny(requestID)
	if err != nil {
		return status, errors.New("Pairing could not be canceled. Lock storage and preserve existing state before retrying.")
	}
	return status, nil
}

// ServiceShutdown is Wails lifecycle-only and excluded from generated bindings.
func (s *Service) ServiceShutdown() error {
	if s.LockStorage().StorageState != "locked" {
		return errors.New("Local storage lock could not be released")
	}
	return nil
}
