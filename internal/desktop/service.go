// Package desktop is the complete, deliberately narrow Wails application bridge.
// Identity, pairing, chat, transport and vault objects are never bound directly.
package desktop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"whatsup/internal/call"
	"whatsup/internal/chat"
	"whatsup/internal/diagnostics"
	"whatsup/internal/identity"
	"whatsup/internal/messaging"
	"whatsup/internal/pairflow"
	"whatsup/internal/storage"
)

const Version = "0.2.1"

type Status struct {
	Version              string `json:"version"`
	IdentityFingerprint  string `json:"identityFingerprint"`
	StorageState         string `json:"storageState"`
	NetworkState         string `json:"networkState"`
	DirectOnlyAvailable  bool   `json:"directOnlyAvailable"`
	PairingAvailable     bool   `json:"pairingAvailable"`
	MessagingAvailable   bool   `json:"messagingAvailable"`
	AttachmentsAvailable bool   `json:"attachmentsAvailable"`
	RequiresPassphrase   bool   `json:"requiresPassphrase"`
}

type Service struct {
	mu                sync.Mutex
	directory         string
	keys              storage.KeyStore
	vault             *storage.Vault
	application       *storage.ApplicationState
	pairing           *pairflow.Coordinator
	invitations       *invitationControl
	messages          *messaging.Registry
	revisions         *messaging.Revisions
	events            *contactEvents
	pairingEvents     *pairingEvents
	preferenceState   *appPreferenceState
	preferenceOptions AppPreferenceRuntimeOptions
	runtimeOptions    RuntimeOptions
	attachments       *attachmentControl
	security          *securityControl
	backupAttention   *storage.ManagedCopyError
	fingerprint       string
	state             string
	record            func(diagnostics.Event)
	// Call state has its own locks so it can be settled from the messaging
	// notification goroutine and while Service.mu is held during lock.
	calls              callControl
	callOptionsMu      sync.Mutex
	callRuntimeOptions RuntimeOptions
	// Test seams for the requests that reach the session loop. Nil in the
	// application.
	callEndForTest     func(context.Context, *messaging.Registry, string, call.ID) (call.Snapshot, error)
	callMuteForTest    func(context.Context, *messaging.Registry, string, call.ID, bool) (call.Snapshot, error)
	callEnforceForTest func(context.Context, *messaging.Registry)
	callPathForTest    func(contactID string) string
	// callRefreshForTest runs between a refresh reading a call's state and
	// recording it, which is where a slow refresh used to be overtaken.
	callRefreshForTest atomic.Pointer[func()]
}

func New(directory string, keys storage.KeyStore, listeners ...func(diagnostics.Event)) *Service {
	record := func(diagnostics.Event) {}
	if len(listeners) != 0 && listeners[0] != nil {
		record = listeners[0]
	}
	return &Service{directory: directory, keys: keys, state: "locked", record: record, revisions: &messaging.Revisions{}}
}

// NewWithEvents adds a backend-only event sink. The renderer never receives an
// emitter capability. Delivery runs outside Service and runtime locks.
func NewWithEvents(directory string, keys storage.KeyStore, record func(diagnostics.Event), sink func(messaging.Hint)) *Service {
	s := New(directory, keys, record)
	s.events = newContactEvents(sink)
	return s
}

func (s *Service) status() Status {
	status := Status{Version: Version, IdentityFingerprint: s.fingerprint, StorageState: s.state, NetworkState: "unavailable", PairingAvailable: s.state == "unlocked" && s.invitations != nil}
	if protection, err := storage.InspectProtectionWithKeys(s.directory, s.keys); err == nil {
		status.RequiresPassphrase = protection.RequiresPassphrase
	}
	if s.security != nil && s.security.operation != nil && s.security.operation.quiesced {
		status.IdentityFingerprint = ""
		status.StorageState = "locked"
	}
	status.AttachmentsAvailable = s.state == "unlocked" && s.attachments != nil && s.attachments.session != nil
	if s.state == "unlocked" && s.messages != nil {
		contacts, err := s.messages.Contacts()
		if err == nil {
			for _, contact := range contacts {
				status.MessagingAvailable = status.MessagingAvailable || contact.CanSend
				if status.NetworkState == "unavailable" || contact.State == "connected" {
					status.NetworkState = contact.State
				}
			}
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
func makeControllers(s *Service, local *identity.Identity, app *storage.ApplicationState, revisions *messaging.Revisions, notify func(messaging.Hint), options RuntimeOptions, record func(diagnostics.Event)) (*messaging.Registry, *pairflow.Coordinator, *invitationControl, error) {
	// Call state is settled before the general contact hint is passed on, so
	// the capture gate and the call view are current when the interface reacts.
	var registry atomic.Pointer[messaging.Registry]
	observed := func(hint messaging.Hint) {
		s.observeCall(registry.Load(), hint.ContactID)
		notify(hint)
	}
	messages, err := messaging.NewRegistry(local, app, messaging.RegistryOptions{Revisions: revisions, OnChanged: observed, OnIncoming: options.OnIncoming, Diagnostics: record, Factories: options.Factories, Calls: s.callOptions()})
	if err != nil {
		return nil, nil, nil, err
	}
	registry.Store(messages)
	flow, err := pairflow.New(local, app, pairflow.Options{
		CompleteActivation: true,
		OnCompleted:        func(id string) { _, _ = messages.Connect(id) },
		OnChanged: func() {
			if options.OnPairingChanged != nil {
				options.OnPairingChanged(PairingHint{})
			}
		},
	})
	if err != nil {
		messages.Close()
		return nil, nil, nil, err
	}
	control := newInvitationControl(local, flow)
	control.onChanged = func() {
		if options.OnPairingChanged != nil {
			options.OnPairingChanged(PairingHint{})
		}
	}
	return messages, flow, control, nil
}

// UnlockStorage opens OS-protected identity and approved local chat state. The
// native construction policy automatically connects saved approved contacts;
// component constructors remain offline unless that policy is explicitly set.
// Unlock never emits an invitation or grants a pending pairing chat authority.
func (s *Service) UnlockStorage() (Status, error) {
	return s.unlockStorage(nil)
}

func (s *Service) unlockStorage(secret []byte) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unlockStorageLocked(secret)
}

func (s *Service) managedCopyAttention(err error) error {
	var copyError *storage.ManagedCopyError
	if !errors.As(err, &copyError) {
		return nil
	}
	captured := *copyError
	s.backupAttention = &captured
	if copyError.Repairable {
		return fmt.Errorf("A local backup requires attention: %s. Its bytes were preserved. Use Security & recovery to securely set it aside and retry.", filepath.ToSlash(copyError.RelativePath))
	}
	return fmt.Errorf("A local backup requires attention: %s. Its bytes were preserved. Check this item's ownership, private permissions and availability before retrying. WhatsUp cannot safely set it aside.", filepath.ToSlash(copyError.RelativePath))
}

func (s *Service) unlockStorageLocked(secret []byte) (Status, error) {
	if s.security != nil && s.security.options.Life != nil && s.security.options.Life.Err() != nil {
		return s.status(), errors.New("The desktop is shutting down")
	}
	if s.security != nil && s.security.operation != nil {
		return s.status(), errors.New("A local security operation is in progress")
	}
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
	vault, err := storage.OpenWithPassphrase(s.directory, s.keys, secret)
	if err != nil {
		if attention := s.managedCopyAttention(err); attention != nil {
			s.state = "unavailable"
			s.record(diagnostics.StorageUnavailable)
			return s.status(), attention
		}
		if errors.Is(err, storage.ErrPassphraseRequired) {
			s.state = "locked"
			return s.status(), errors.New("Enter the app passphrase to go online")
		}
		if len(secret) != 0 {
			s.state = "locked"
			s.record(diagnostics.StorageUnavailable)
			return s.status(), errors.New("Local storage could not be unlocked. Check the passphrase and OS credential protection.")
		}
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
		if attention := s.managedCopyAttention(err); attention != nil {
			_, _ = fail()
			return s.status(), attention
		}
		if errors.Is(err, storage.ErrPassphrase) || errors.Is(err, storage.ErrPassphraseRequired) {
			if closeErr := vault.Close(); closeErr != nil {
				s.vault = vault
			}
			s.state = "locked"
			return s.status(), errors.New("Local storage could not be unlocked. Check the passphrase and OS credential protection.")
		}
		return fail()
	}
	local, err := application.Identity()
	if err != nil {
		return fail()
	}
	if s.security != nil && s.security.options.Life != nil && s.security.options.Life.Err() != nil {
		return fail()
	}
	messages, pairing, invitations, err := makeControllers(s, local, application, s.revisions, s.events.enqueue, s.runtimeOptions, s.record)
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
	s.backupAttention = nil
	s.startAttachmentsLocked()
	s.record(diagnostics.StorageUnlocked)
	if s.runtimeOptions.AutoConnect {
		if contacts, err := messages.Contacts(); err == nil {
			for _, contact := range contacts {
				if contact.CanConnect {
					_, _ = messages.Connect(contact.ContactID)
				}
			}
		}
	}
	return s.status(), nil
}

func (s *Service) LockStorage() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancelSecurityLocked()
	return s.lockStorageLocked()
}

func (s *Service) failStorageWriteLocked() {
	// A root rename may have reached disk before a sync error. Retire every
	// network generation until an authenticated reopen resolves that state.
	s.cancelSecurityLocked()
	_ = s.lockStorageLocked()
	s.record(diagnostics.StorageUnavailable)
}

func (s *Service) finishStorageCall(wasUnlocked bool) {
	state := s.state
	var emit func(string)
	if wasUnlocked && state != "unlocked" && s.security != nil {
		emit = s.security.options.StorageChanged
	}
	s.mu.Unlock()
	if emit != nil {
		emit(state)
	}
}

func (s *Service) lockStorageLocked() Status {
	s.stopAttachmentsLocked()
	if s.messages != nil {
		s.messages.Close()
		s.messages = nil
	}
	// Closing the registry ended any call and joined its media. Close the
	// capture gate and the interface's media stream before anything else.
	s.resetCalls()
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
	return messaging.Snapshot{State: "unavailable", Path: "Unknown", Messages: []chat.Message{}}
}

// GetConversation returns only the selected contact's bounded local history.
// Polling never starts transport or chooses a relay service.
func (s *Service) GetConversation() messaging.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return unavailableConversation()
	}
	return s.firstConversationLocked()
}

// Kept for older bridge clients; current clients select an exact ID and request
// paged history. Reading the contact list never loads message bodies.
func (s *Service) firstConversationLocked() messaging.Snapshot {
	if s.messages == nil {
		return unavailableConversation()
	}
	contacts, err := s.messages.Contacts()
	if err != nil {
		return unavailableConversation()
	}
	if len(contacts) == 0 {
		return messaging.Snapshot{State: "none", Path: "Unknown", Messages: []chat.Message{}}
	}
	return s.messages.Snapshot(contacts[0].ContactID)
}

// ConnectContact starts automatic authentication/retry for an already approved
// identity. It never creates an invitation or requests fingerprint approval.
func (s *Service) ConnectContact(contactID string) (messaging.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "unlocked" || s.messages == nil {
		return unavailableConversation(), errors.New("Unlock local storage before connecting")
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
	locked := s.LockStorage().StorageState == "locked"
	// Event hooks can call back into the service. Join them only after LockStorage
	// has released Service.mu, including on a failed shutdown.
	s.events.close()
	s.pairingEvents.close()
	prefsErr := CloseAppPreferences(s)
	if !locked {
		return errors.New("Local storage lock could not be released")
	}
	return prefsErr
}
