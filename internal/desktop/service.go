// Package desktop is the complete, deliberately narrow Wails application bridge.
// Identity, pairing, chat, transport and vault objects are never bound directly.
package desktop

import (
	"errors"
	"sync"

	"whatsup/internal/chat"
	"whatsup/internal/diagnostics"
	"whatsup/internal/identity"
	"whatsup/internal/messaging"
	"whatsup/internal/pairflow"
	"whatsup/internal/storage"
)

const Version = "0.0.6"

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
	messages    *messaging.Manager
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
	status := Status{Version: Version, IdentityFingerprint: s.fingerprint, StorageState: s.state, NetworkState: "unavailable", PairingAvailable: s.state == "unlocked" && s.invitations != nil}
	if s.state == "unlocked" && s.messages != nil {
		conversation := s.messages.Snapshot()
		status.MessagingAvailable = conversation.CanSend
		if conversation.ContactID != "" {
			status.NetworkState = conversation.State
		}
	}
	return status
}

func (s *Service) GetStatus() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status()
}

// makeControllers binds automatic post-pairing connection to one exact open
// runtime generation. A late completion cannot select a replacement manager.
// The callback never enters Service.mu: Lock/Forget hold that mutex while
// joining the pairing run, and close messages before doing so.
func makeControllers(local *identity.Identity, app *storage.ApplicationState) (*messaging.Manager, *pairflow.Coordinator, *invitationControl, error) {
	messages, err := messaging.New(local, app)
	if err != nil {
		return nil, nil, nil, err
	}
	flow, err := pairflow.New(local, app, pairflow.Options{
		CompleteActivation: true,
		OnCompleted:        func(id string) { _, _ = messages.Connect(id) },
	})
	if err != nil {
		messages.Close()
		return nil, nil, nil, err
	}
	return messages, flow, newInvitationControl(local, flow), nil
}

// UnlockStorage opens OS-protected local identity and approved local chat state.
// It starts no listener or connection and emits no invitation. Offline queueing
// is independent of the fresh authentication required by each live session.
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
	messages, pairing, invitations, err := makeControllers(local, application)
	if err != nil {
		return fail()
	}
	s.fingerprint = local.PublicKey().Fingerprint()
	s.vault = vault
	s.application = application
	s.pairing = pairing
	s.invitations = invitations
	s.messages = messages
	s.state = "unlocked"
	s.record(diagnostics.StorageUnlocked)
	return s.status(), nil
}

func (s *Service) LockStorage() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.messages != nil {
		s.messages.Close()
		s.messages = nil
	}
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

func unavailableConversation() messaging.Snapshot {
	return messaging.Snapshot{State: "unavailable", Messages: []chat.Message{}}
}

// GetConversation returns only the selected contact's bounded local history.
// Polling never starts transport or chooses a relay service.
func (s *Service) GetConversation() messaging.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return unavailableConversation()
	}
	return s.messages.Snapshot()
}

// ConnectContact starts automatic authentication/retry for an already approved
// identity. It never creates an invitation or requests fingerprint approval.
func (s *Service) ConnectContact(contactID string) (messaging.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return unavailableConversation(), errors.New("Unlock local storage before connecting")
	}
	if s.pairing != nil {
		state := s.pairing.Snapshot().State
		if state == "connecting" || state == "awaiting-approval" || state == "waiting-for-peer" || state == "saving" {
			return s.messages.Snapshot(), errors.New("Finish the current pairing before connecting")
		}
	}
	status, err := s.messages.Connect(contactID)
	if err != nil {
		return status, errors.New("This approved contact could not be connected. Disconnect and unlock before retrying.")
	}
	return status, nil
}

func (s *Service) SendMessage(contactID, text string) (messaging.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return unavailableConversation(), errors.New("Unlock local storage before sending")
	}
	status, err := s.messages.Send(contactID, text)
	if err != nil {
		return status, errors.New("The message could not be saved. Check its size and text, or disconnect and unlock before retrying.")
	}
	return status, nil
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

// ApprovePairing confirms the current reserved identity. It returns public state
// only; successful reciprocal durable completion later starts this generation's
// saved connection through the coordinator callback.
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
