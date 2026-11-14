package storage

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"time"

	"whatsup/internal/identity"
)

const (
	ApplicationSchemaVersion = 2
	MaxContacts              = 32
	maxChatBytes             = 2 << 20
	maxChatMessages          = 256
	maxChatTextBytes         = 256 << 10
)

var (
	ErrScope         = errors.New("raw vault access is unavailable while application sections are open")
	ErrState         = errors.New("application state is invalid or unsupported")
	ErrStateFailed   = errors.New("application persistence failed; close and reopen before retrying")
	ErrContact       = errors.New("a matching locally approved contact pin is required")
	ErrCapacity      = errors.New("application state exceeds its safety limit")
	ErrStateConflict = errors.New("chat state changed through another scoped writer; reopen before retrying")
	ErrScopeUsed     = errors.New("chat store is already checked out; obtain a fresh scoped handle")
)

type identitySection struct {
	Version int    `json:"version"`
	Seed    []byte `json:"seed"`
}

// A pin records only local approval metadata. It cannot restore a Contact,
// transport credentials, reciprocal authentication, or live session authority.
type contactSection struct {
	Version    int                `json:"version"`
	Local      identity.PublicKey `json:"local"`
	Peer       identity.PublicKey `json:"peer"`
	Chat       json.RawMessage    `json:"chat,omitempty"`
	generation uint64
}

type applicationEnvelope struct {
	Version  int              `json:"version"`
	Identity identitySection  `json:"identity"`
	Contacts []contactSection `json:"contacts"`
}

// ApprovedContact is implemented by pairing.Contact without making storage
// depend on transport code. It represents local approval only. Implementations
// must retain their authorization guard for the bounded operation callback.
type ApprovedContact interface {
	Local() identity.PublicKey
	Peer() identity.PublicKey
	WithAuthenticatedPeer(identity.PublicKey, func() error) error
}

func nilContact(contact ApprovedContact) bool {
	if contact == nil {
		return true
	}
	value := reflect.ValueOf(contact)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	}
	return false
}

// ApplicationState owns all sections of one encrypted Vault. The vault mutex
// covers every read-modify-durable-write and all adapters, including repeated
// OpenApplication calls. Raw Vault.Load/Save are disabled after claiming it.
// No object in this file is a Wails binding or a generic filesystem API.
type ApplicationState struct {
	vault  *Vault
	state  applicationEnvelope
	failed bool
	// Uses Vault.saveLocked in production. Injectable only inside this package
	// for deterministic failures before/after a durable replacement in tests.
	save func([]byte) error
}

func (a ApplicationState) String() string   { return "[private WhatsUp application state]" }
func (a ApplicationState) GoString() string { return a.String() }

// OpenApplication creates a new identity only when no vault snapshot exists.
// Schema 1 identity snapshots migrate without changing the seed. Unsupported,
// duplicate-field, noncanonical, or malformed state is preserved and rejected.
func OpenApplication(vault *Vault) (*ApplicationState, error) {
	if vault == nil {
		return nil, ErrLocked
	}
	vault.mu.Lock()
	defer vault.mu.Unlock()
	if vault.closed {
		return nil, ErrLocked
	}
	if vault.application != nil {
		if vault.application.failed {
			return nil, ErrStateFailed
		}
		return vault.application, nil
	}
	a := &ApplicationState{vault: vault, save: vault.saveLocked}
	payload, err := vault.loadLocked()
	defer clear(payload)
	write := false
	if os.IsNotExist(err) {
		local, generateErr := identity.Generate()
		if generateErr != nil {
			return nil, generateErr
		}
		seed := local.Seed()
		a.state = applicationEnvelope{Version: ApplicationSchemaVersion, Identity: identitySection{Version: 1, Seed: bytes.Clone(seed[:])}, Contacts: []contactSection{}}
		clear(seed[:])
		write = true
	} else if err != nil {
		return nil, err
	} else {
		// Dispatch only on the version, then require the complete exact schema.
		var version struct {
			Version int `json:"version"`
		}
		if json.Unmarshal(payload, &version) != nil {
			return nil, ErrState
		}
		switch version.Version {
		case 1:
			var old struct {
				Version int    `json:"version"`
				Seed    []byte `json:"identitySeed"`
			}
			if decodeCanonical(payload, &old) != nil || len(old.Seed) != 32 {
				clear(old.Seed)
				return nil, ErrState
			}
			a.state = applicationEnvelope{Version: ApplicationSchemaVersion, Identity: identitySection{Version: 1, Seed: old.Seed}, Contacts: []contactSection{}}
			write = true
		case ApplicationSchemaVersion:
			if decodeCanonical(payload, &a.state) != nil {
				a.clearLocked()
				return nil, ErrState
			}
		default:
			return nil, ErrState
		}
	}
	if validateEnvelope(a.state) != nil {
		a.clearLocked()
		return nil, ErrState
	}
	// Claim before the first save. An uncertain creation/migration must not be
	// retried against a possibly replaced snapshot in this same open Vault.
	vault.application = a
	if write {
		if err := a.commitLocked(a.state); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// Identity returns a backend-only copy. Only its public fingerprint belongs in
// status DTOs; neither the application envelope nor seed may cross the bridge.
func (a *ApplicationState) Identity() (*identity.Identity, error) {
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return nil, err
	}
	return identity.FromSeed(a.state.Identity.Seed)
}

// PinApprovedContact persists one local approval pin before any chat section can
// be opened. It does not activate transport or claim completed mutual approval.
func (a *ApplicationState) PinApprovedContact(contact ApprovedContact) error {
	if nilContact(contact) {
		return ErrContact
	}
	localPin, peerPin := contact.Local(), contact.Peer()
	return contact.WithAuthenticatedPeer(peerPin, func() error {
		a.vault.mu.Lock()
		defer a.vault.mu.Unlock()
		if err := a.usableLocked(); err != nil {
			return err
		}
		local, err := identity.FromSeed(a.state.Identity.Seed)
		if err != nil || !localPin.Equal(local.PublicKey()) || !peerPin.Valid() || peerPin.Equal(localPin) {
			return ErrContact
		}
		if a.findContactLocked(localPin, peerPin) >= 0 {
			return nil
		}
		if len(a.state.Contacts) >= MaxContacts {
			return ErrCapacity
		}
		next := a.cloneLocked()
		next.Contacts = append(next.Contacts, contactSection{Version: 1, Local: localPin, Peer: peerPin})
		return a.commitLocked(next)
	})
}

// ChatStore is an independent snapshot scope, not a raw vault handle. The chat
// domain must continue holding its Contact authorization guard on each operation.
// Avoid reacquiring that guard here: a pending revocation writer would deadlock
// nested RWMutex readers. Construction itself requires active local approval.
// Each handle permits one Load checkout and then repeated durable Save calls.
// Obtain a fresh handle for each chat.Open: sharing one handle between two live
// Conversation instances would otherwise bypass stale-snapshot generation checks.
type ChatStore struct {
	application *ApplicationState
	local       identity.PublicKey
	peer        identity.PublicKey
	generation  uint64
	loaded      bool
}

func (s ChatStore) String() string   { return "[private WhatsUp scoped chat store]" }
func (s ChatStore) GoString() string { return s.String() }

func (a *ApplicationState) ChatStore(contact ApprovedContact) (*ChatStore, error) {
	if nilContact(contact) {
		return nil, ErrContact
	}
	localPin, peerPin := contact.Local(), contact.Peer()
	var store *ChatStore
	err := contact.WithAuthenticatedPeer(peerPin, func() error {
		a.vault.mu.Lock()
		defer a.vault.mu.Unlock()
		if err := a.usableLocked(); err != nil {
			return err
		}
		index := a.findContactLocked(localPin, peerPin)
		if index < 0 {
			return ErrContact
		}
		store = &ChatStore{application: a, local: localPin, peer: peerPin, generation: a.state.Contacts[index].generation}
		return nil
	})
	return store, err
}

func (s *ChatStore) Load() ([]byte, error) {
	a := s.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return nil, err
	}
	if s.loaded {
		return nil, ErrScopeUsed
	}
	index := a.findContactLocked(s.local, s.peer)
	if index < 0 {
		return nil, ErrContact
	}
	s.loaded = true
	if len(a.state.Contacts[index].Chat) == 0 {
		s.generation = a.state.Contacts[index].generation
		return nil, os.ErrNotExist
	}
	s.generation = a.state.Contacts[index].generation
	return bytes.Clone(a.state.Contacts[index].Chat), nil
}

func (s *ChatStore) Save(payload []byte) error {
	a := s.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return err
	}
	index := a.findContactLocked(s.local, s.peer)
	if index < 0 {
		return ErrContact
	}
	if a.state.Contacts[index].generation != s.generation {
		return ErrStateConflict
	}
	if validateChat(payload, s.local, s.peer) != nil {
		return ErrState
	}
	next := a.cloneLocked()
	next.Contacts[index].Chat = bytes.Clone(payload)
	next.Contacts[index].generation++
	if err := a.commitLocked(next); err != nil {
		return err
	}
	s.generation = next.Contacts[index].generation
	return nil
}

func (a *ApplicationState) Close() error { return a.vault.Close() }

func (a *ApplicationState) usableLocked() error {
	if a.vault.closed {
		return ErrLocked
	}
	if a.failed {
		return ErrStateFailed
	}
	return nil
}

func (a *ApplicationState) findContactLocked(local, peer identity.PublicKey) int {
	for index, contact := range a.state.Contacts {
		if contact.Local.Equal(local) && contact.Peer.Equal(peer) {
			return index
		}
	}
	return -1
}

func (a *ApplicationState) cloneLocked() applicationEnvelope {
	next := a.state
	next.Contacts = append([]contactSection{}, a.state.Contacts...)
	return next
}

func (a *ApplicationState) commitLocked(next applicationEnvelope) error {
	if err := validateEnvelope(next); err != nil {
		return err
	}
	payload, err := json.Marshal(next)
	if err != nil || len(payload) > MaxStateBytes {
		clear(payload)
		return ErrCapacity
	}
	defer clear(payload)
	if err := a.save(payload); err != nil {
		// A rename may already have succeeded before a sync failure. Never
		// publish speculative memory or permit another writer until reopening.
		a.failed = true
		return errors.Join(ErrStateFailed, err)
	}
	a.state = next
	return nil
}

func (a *ApplicationState) clearLocked() {
	clear(a.state.Identity.Seed)
	for index := range a.state.Contacts {
		clear(a.state.Contacts[index].Chat)
	}
	a.state = applicationEnvelope{}
}

func validateEnvelope(state applicationEnvelope) error {
	if state.Version != ApplicationSchemaVersion || state.Identity.Version != 1 || len(state.Identity.Seed) != 32 || state.Contacts == nil || len(state.Contacts) > MaxContacts {
		return ErrState
	}
	local, err := identity.FromSeed(state.Identity.Seed)
	if err != nil {
		return ErrState
	}
	seen := make(map[identity.PublicKey]bool, len(state.Contacts))
	for _, contact := range state.Contacts {
		if contact.Version != 1 || !contact.Local.Equal(local.PublicKey()) || !contact.Peer.Valid() || contact.Peer.Equal(contact.Local) || seen[contact.Peer] {
			return ErrState
		}
		seen[contact.Peer] = true
		if len(contact.Chat) != 0 && validateChat(contact.Chat, contact.Local, contact.Peer) != nil {
			return ErrState
		}
	}
	return nil
}

// These structural DTOs bound the chat section without granting storage a chat
// capability. Semantic protocol validation remains in chat; keep these schema-1
// fields/limits aligned with it. Canonical decoding rejects unknown/duplicate
// fields, invalid UTF-8 replacements, trailing data and noncanonical encodings.
type storedChat struct {
	Version  int                `json:"version"`
	Local    identity.PublicKey `json:"local"`
	Peer     identity.PublicKey `json:"peer"`
	Messages []storedMessage    `json:"messages"`
	Outbox   []storedAttempt    `json:"outbox"`
}
type storedMessage struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	Delivered bool      `json:"delivered"`
}
type storedAttempt struct {
	ID          string    `json:"id"`
	Count       uint32    `json:"count"`
	NextAttempt time.Time `json:"nextAttempt"`
}

func validateChat(payload []byte, local, peer identity.PublicKey) error {
	if len(payload) == 0 || len(payload) > maxChatBytes {
		return ErrState
	}
	var state storedChat
	if decodeCanonical(payload, &state) != nil || state.Version != 1 || !state.Local.Equal(local) || !state.Peer.Equal(peer) || len(state.Messages) > maxChatMessages || len(state.Outbox) > maxChatMessages {
		return ErrState
	}
	total := 0
	seen := make(map[string]storedMessage, len(state.Messages))
	for _, message := range state.Messages {
		id, err := base64.RawURLEncoding.Strict().DecodeString(message.ID)
		if err != nil || len(id) != 16 || base64.RawURLEncoding.EncodeToString(id) != message.ID || message.CreatedAt.IsZero() || len(message.Text) == 0 || len(message.Text) > 16<<10 || (message.Direction != "incoming" && message.Direction != "outgoing") || (message.Direction == "incoming" && !message.Delivered) {
			return ErrState
		}
		if _, duplicate := seen[message.ID]; duplicate {
			return ErrState
		}
		seen[message.ID] = message
		total += len(message.Text)
	}
	if total > maxChatTextBytes {
		return ErrCapacity
	}
	pending := make(map[string]bool, len(state.Outbox))
	for _, attempt := range state.Outbox {
		message, exists := seen[attempt.ID]
		if !exists || message.Direction != "outgoing" || message.Delivered || pending[attempt.ID] || attempt.NextAttempt.IsZero() {
			return ErrState
		}
		pending[attempt.ID] = true
	}
	for _, message := range state.Messages {
		if message.Direction == "outgoing" && message.Delivered == pending[message.ID] {
			return ErrState
		}
	}
	return nil
}

func decodeCanonical(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrState
	}
	canonical, err := json.Marshal(target)
	defer clear(canonical)
	if err != nil || !bytes.Equal(payload, canonical) {
		return ErrState
	}
	return nil
}
