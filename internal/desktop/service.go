// Package desktop is the complete, deliberately narrow Wails application bridge.
// Identity, pairing, chat, transport and vault objects are never bound directly.
package desktop

import (
	"errors"
	"sync"

	"whatsup/internal/diagnostics"
	"whatsup/internal/storage"
)

const Version = "0.1.0"

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
	return Status{Version: Version, IdentityFingerprint: s.fingerprint, StorageState: s.state, NetworkState: "unavailable"}
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
	s.fingerprint = local.PublicKey().Fingerprint()
	s.vault = vault
	s.application = application
	s.state = "unlocked"
	s.record(diagnostics.StorageUnlocked)
	return s.status(), nil
}

func (s *Service) LockStorage() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
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

// ServiceShutdown is Wails lifecycle-only and excluded from generated bindings.
func (s *Service) ServiceShutdown() error {
	if s.LockStorage().StorageState != "locked" {
		return errors.New("Local storage lock could not be released")
	}
	return nil
}
