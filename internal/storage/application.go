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

	"whatsup/internal/attachmentstore"
	"whatsup/internal/identity"
	"whatsup/internal/messagestore"
	"whatsup/internal/profile"
)

const (
	ApplicationSchemaVersion = 8
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
	ContactID  string             `json:"contactID,omitempty"`
	Chat       json.RawMessage    `json:"chat,omitempty"`
	LocalName  string             `json:"localName,omitempty"`
	PeerName   string             `json:"peerDisplayName,omitempty"`
	StoreID    string             `json:"storeID"`
	generation uint64
}

// Schema 6 has no SQLite scopes or migration state. Decode its exact shape;
// newer authorization fields must never be accepted under an older version.
type contactSectionV6 struct {
	Version   int                `json:"version"`
	Local     identity.PublicKey `json:"local"`
	Peer      identity.PublicKey `json:"peer"`
	ContactID string             `json:"contactID,omitempty"`
	Chat      json.RawMessage    `json:"chat,omitempty"`
	LocalName string             `json:"localName,omitempty"`
	PeerName  string             `json:"peerDisplayName,omitempty"`
}
type applicationEnvelopeV6 struct {
	Version           int                 `json:"version"`
	Identity          identitySection     `json:"identity"`
	Profile           profile.Local       `json:"profile"`
	Contacts          []contactSectionV6  `json:"contacts"`
	PendingContacts   []pendingRecordDisk `json:"pendingContacts"`
	CommittedContacts []pendingRecordDisk `json:"committedContacts"`
}

// Schema 5 has no profile or contact-name fields. Keep its exact shape so a
// version-5 snapshot cannot smuggle new metadata into a migration.
type contactSectionV5 struct {
	Version   int                `json:"version"`
	Local     identity.PublicKey `json:"local"`
	Peer      identity.PublicKey `json:"peer"`
	ContactID string             `json:"contactID,omitempty"`
	Chat      json.RawMessage    `json:"chat,omitempty"`
}

type applicationEnvelopeV5 struct {
	Version           int                 `json:"version"`
	Identity          identitySection     `json:"identity"`
	Contacts          []contactSectionV5  `json:"contacts"`
	PendingContacts   []pendingRecordDisk `json:"pendingContacts"`
	CommittedContacts []pendingRecordDisk `json:"committedContacts"`
}

// Exact old shapes are kept separate: migration must never accept a new
// connection policy or contact-ID authorization under an older schema number.
type contactSectionV1 struct {
	Version int                `json:"version"`
	Local   identity.PublicKey `json:"local"`
	Peer    identity.PublicKey `json:"peer"`
	Chat    json.RawMessage    `json:"chat,omitempty"`
}
type pendingRecordDiskV1 struct {
	Version          int                `json:"version"`
	ID               string             `json:"id"`
	Role             string             `json:"role"`
	Local            identity.PublicKey `json:"local"`
	Peer             identity.PublicKey `json:"peer"`
	LocalNodePrivate string             `json:"localNodePrivate"`
	LocalNode        string             `json:"localNode"`
	PeerNode         string             `json:"peerNode"`
	PresharedKey     string             `json:"presharedKey"`
}

type applicationEnvelopeV4 struct {
	Version           int                   `json:"version"`
	Identity          identitySection       `json:"identity"`
	Contacts          []contactSectionV1    `json:"contacts"`
	PendingContacts   []pendingRecordDiskV1 `json:"pendingContacts"`
	CommittedContacts []pendingRecordDiskV1 `json:"committedContacts"`
}

type applicationEnvelope struct {
	Version             int                 `json:"version"`
	Identity            identitySection     `json:"identity"`
	Profile             profile.Local       `json:"profile"`
	Contacts            []contactSection    `json:"contacts"`
	PendingContacts     []pendingRecordDisk `json:"pendingContacts"`
	CommittedContacts   []pendingRecordDisk `json:"committedContacts"`
	MessageStoreState   string              `json:"messageStoreState"`
	MigrationBackup     string              `json:"migrationBackup,omitempty"`
	MigrationBackupHash string              `json:"migrationBackupSHA256,omitempty"`
	DeleteScopes        []string            `json:"deleteScopes"`
	Routes              []routeStateDisk    `json:"routes"`
}

// Schema 3 is decoded exactly before adding the committed section.
type applicationEnvelopeV3 struct {
	Version         int                   `json:"version"`
	Identity        identitySection       `json:"identity"`
	Contacts        []contactSectionV1    `json:"contacts"`
	PendingContacts []pendingRecordDiskV1 `json:"pendingContacts"`
}

// The canonical schema-2 shape must stay exact during migration. Adding a
// section to it before validation would accept unknown or ambiguous old state.
type applicationEnvelopeV2 struct {
	Version  int                `json:"version"`
	Identity identitySection    `json:"identity"`
	Contacts []contactSectionV1 `json:"contacts"`
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
	save                 func([]byte) error
	commitReceipts       map[string]*commitReceiptState
	nextCommitGeneration uint64
	localReceipts        map[string]*localContactReceiptState
	messages             *messagestore.DB
	attachments          *attachmentstore.Store
}

func (a ApplicationState) String() string   { return "[private WhatsUp application state]" }
func (a ApplicationState) GoString() string { return a.String() }

// OpenApplication creates a new identity only when no vault snapshot exists.
// Schema 1/2/3/4/5 snapshots migrate without changing the seed or existing chat. Unsupported,
// duplicate-field, noncanonical, or malformed state is preserved and rejected.
func OpenApplication(vault *Vault) (*ApplicationState, error) {
	if vault == nil || vault.vaultState == nil {
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
	a := &ApplicationState{vault: vault, save: vault.saveLocked, commitReceipts: make(map[string]*commitReceiptState)}
	payload, err := vault.loadLocked()
	defer clear(payload)
	write := false
	oldSchema := false
	currentSchema := false
	metadataMigration := false
	if os.IsNotExist(err) {
		local, generateErr := identity.Generate()
		if generateErr != nil {
			return nil, generateErr
		}
		seed := local.Seed()
		a.state = applicationEnvelope{Version: ApplicationSchemaVersion, Identity: identitySection{Version: 1, Seed: bytes.Clone(seed[:])}, Profile: profile.Local{Revision: 1}, Contacts: []contactSection{}, PendingContacts: []pendingRecordDisk{}, CommittedContacts: []pendingRecordDisk{}, MessageStoreState: "pending", DeleteScopes: []string{}}
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
			a.state = applicationEnvelope{Version: ApplicationSchemaVersion, Identity: identitySection{Version: 1, Seed: old.Seed}, Profile: profile.Local{Revision: 1}, Contacts: []contactSection{}, PendingContacts: []pendingRecordDisk{}, CommittedContacts: []pendingRecordDisk{}}
			write = true
		case 2:
			var old applicationEnvelopeV2
			if decodeCanonical(payload, &old) != nil {
				clear(old.Identity.Seed)
				for index := range old.Contacts {
					clear(old.Contacts[index].Chat)
				}
				return nil, ErrState
			}
			a.state = migrateLegacy(old.Identity, old.Contacts, []pendingRecordDiskV1{}, []pendingRecordDiskV1{})
			write = true
		case 3:
			var old applicationEnvelopeV3
			if decodeCanonical(payload, &old) != nil {
				clear(old.Identity.Seed)
				for index := range old.Contacts {
					clear(old.Contacts[index].Chat)
				}
				clear(old.PendingContacts)
				return nil, ErrState
			}
			a.state = migrateLegacy(old.Identity, old.Contacts, old.PendingContacts, []pendingRecordDiskV1{})
			write = true
		case 4:
			var old applicationEnvelopeV4
			if decodeCanonical(payload, &old) != nil {
				clear(old.Identity.Seed)
				return nil, ErrState
			}
			a.state = migrateLegacy(old.Identity, old.Contacts, old.PendingContacts, old.CommittedContacts)
			write = true
		case 5:
			var old applicationEnvelopeV5
			if decodeCanonical(payload, &old) != nil {
				clear(old.Identity.Seed)
				return nil, ErrState
			}
			a.state = applicationEnvelope{Version: ApplicationSchemaVersion, Identity: old.Identity, Profile: profile.Local{Revision: 1}, PendingContacts: old.PendingContacts, CommittedContacts: old.CommittedContacts}
			if old.Contacts != nil {
				a.state.Contacts = make([]contactSection, len(old.Contacts))
				for i, c := range old.Contacts {
					a.state.Contacts[i] = contactSection{Version: c.Version, Local: c.Local, Peer: c.Peer, ContactID: c.ContactID, Chat: c.Chat}
				}
			}
			write = true
		case 6:
			var old applicationEnvelopeV6
			if decodeCanonical(payload, &old) != nil {
				return nil, ErrState
			}
			a.state = applicationEnvelope{Version: ApplicationSchemaVersion, Identity: old.Identity, Profile: old.Profile, PendingContacts: old.PendingContacts, CommittedContacts: old.CommittedContacts}
			if old.Contacts != nil {
				a.state.Contacts = make([]contactSection, len(old.Contacts))
				for index, c := range old.Contacts {
					a.state.Contacts[index] = contactSection{Version: c.Version, Local: c.Local, Peer: c.Peer, ContactID: c.ContactID, Chat: c.Chat, LocalName: c.LocalName, PeerName: c.PeerName}
				}
			}
			write = true
		case 7:
			var old applicationEnvelopeV7
			if decodeCanonical(payload, &old) != nil {
				return nil, ErrState
			}
			a.state = upgradeEnvelopeV7(old)
			write = true
			metadataMigration = true
		case ApplicationSchemaVersion:
			currentSchema = true
			if decodeCanonical(payload, &a.state) != nil {
				a.clearLocked()
				return nil, ErrState
			}
		default:
			return nil, ErrState
		}
		oldSchema = version.Version <= 6
	}
	if !currentSchema && a.state.Routes == nil {
		a.state.Routes = []routeStateDisk{}
	}
	if oldSchema {
		if err := a.prepareMessageMigrationLocked(); err != nil {
			a.clearLocked()
			return nil, err
		}
	}
	if metadataMigration {
		if err := a.backupRootMetadataLocked(); err != nil {
			a.clearLocked()
			return nil, err
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
	if err := a.openMessageStoreLocked(); err != nil {
		a.failed = true
		_ = a.closeMessageStoreLocked()
		return nil, err
	}
	if err := a.openAttachmentStoreLocked(); err != nil {
		a.failed = true
		_ = a.closeAttachmentStoreLocked()
		_ = a.closeMessageStoreLocked()
		return nil, err
	}
	if err := a.finishMessageDeletesLocked(); err != nil {
		a.failed = true
		_ = a.closeAttachmentStoreLocked()
		_ = a.closeMessageStoreLocked()
		return nil, err
	}
	if err := a.finishRecoveryLocked(); err != nil {
		a.failed = true
		_ = a.closeAttachmentStoreLocked()
		_ = a.closeMessageStoreLocked()
		return nil, err
	}
	// Repair managed roots missed by older versions only after authenticated
	// migrations/recovery have finished validating their exact backup hashes.
	if err := a.finishManagedCopiesLocked(); err != nil {
		a.failed = true
		_ = a.closeAttachmentStoreLocked()
		_ = a.closeMessageStoreLocked()
		return nil, err
	}
	return a, nil
}

func migrateLegacy(local identitySection, contacts []contactSectionV1, pending, committed []pendingRecordDiskV1) applicationEnvelope {
	state := applicationEnvelope{Version: ApplicationSchemaVersion, Identity: local, Profile: profile.Local{Revision: 1}}
	if contacts != nil {
		state.Contacts = make([]contactSection, len(contacts))
		for i, c := range contacts {
			state.Contacts[i] = contactSection{Version: c.Version, Local: c.Local, Peer: c.Peer, Chat: c.Chat}
		}
	}
	convert := func(old []pendingRecordDiskV1) []pendingRecordDisk {
		if old == nil {
			return nil
		}
		result := make([]pendingRecordDisk, len(old))
		for i, r := range old {
			result[i] = pendingRecordDisk{Version: r.Version, ID: r.ID, Role: r.Role, Local: r.Local, Peer: r.Peer, LocalNodePrivate: r.LocalNodePrivate, LocalNode: r.LocalNode, PeerNode: r.PeerNode, PresharedKey: r.PresharedKey}
		}
		return result
	}
	state.PendingContacts, state.CommittedContacts = convert(pending), convert(committed)
	return state
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
		storeID, err := randomStoreID()
		if err != nil {
			return err
		}
		next.Contacts = append(next.Contacts, contactSection{Version: 1, Local: localPin, Peer: peerPin, StoreID: storeID})
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
	contactID   string
	storeID     string
	repository  *messagestore.Scope
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
		section := a.state.Contacts[index]
		id := ""
		if scoped, ok := contact.(interface{ ContactID() string }); ok {
			id = scoped.ContactID()
		}
		if section.ContactID != id {
			return ErrContact
		}
		if a.messages == nil || a.state.MessageStoreState != "ready" {
			return ErrState
		}
		repository, err := a.messages.Scope(section.StoreID)
		if err != nil {
			return err
		}
		store = &ChatStore{application: a, local: localPin, peer: peerPin, contactID: id, storeID: section.StoreID, generation: section.generation, repository: repository}
		return nil
	})
	return store, err
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
	next.PendingContacts = append([]pendingRecordDisk{}, a.state.PendingContacts...)
	next.CommittedContacts = append([]pendingRecordDisk{}, a.state.CommittedContacts...)
	next.DeleteScopes = append([]string{}, a.state.DeleteScopes...)
	next.Routes = make([]routeStateDisk, len(a.state.Routes))
	for i, r := range a.state.Routes {
		next.Routes[i] = cloneRoute(r)
	}
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
	clear(a.state.PendingContacts)
	clear(a.state.CommittedContacts)
	clear(a.commitReceipts)
	clear(a.localReceipts)
	clear(a.state.Routes)
	a.state = applicationEnvelope{}
}

func validateEnvelope(state applicationEnvelope) error {
	if state.Routes == nil || len(state.Routes) > MaxContacts {
		return ErrState
	}
	if state.Version != ApplicationSchemaVersion || state.Identity.Version != 1 || len(state.Identity.Seed) != 32 || state.Contacts == nil || len(state.Contacts) > MaxContacts || state.PendingContacts == nil || len(state.PendingContacts) > MaxPendingContacts || state.CommittedContacts == nil || len(state.CommittedContacts) > MaxCommittedContacts {
		return ErrState
	}
	if (state.MessageStoreState != "pending" && state.MessageStoreState != "ready") || state.DeleteScopes == nil || len(state.DeleteScopes) > MaxContacts {
		return ErrState
	}
	for _, id := range state.DeleteScopes {
		if !pendingIDValid(id) {
			return ErrState
		}
	}
	if state.Profile.Revision == 0 || profile.ValidateName(state.Profile.DisplayName) != nil {
		return ErrState
	}
	local, err := identity.FromSeed(state.Identity.Seed)
	if err != nil {
		return ErrState
	}
	seen := make(map[identity.PublicKey]bool, len(state.Contacts))
	stores := make(map[string]bool, len(state.Contacts))
	for _, contact := range state.Contacts {
		if !pendingIDValid(contact.StoreID) || stores[contact.StoreID] || (contact.Version == 2 && contact.StoreID != contact.ContactID) {
			return ErrState
		}
		stores[contact.StoreID] = true
		if ((contact.Version != 1 || contact.ContactID != "") && (contact.Version != 2 || !pendingIDValid(contact.ContactID))) || !contact.Local.Equal(local.PublicKey()) || !contact.Peer.Valid() || contact.Peer.Equal(contact.Local) || seen[contact.Peer] {
			return ErrState
		}
		seen[contact.Peer] = true
		if profile.ValidateName(contact.LocalName) != nil || profile.ValidateName(contact.PeerName) != nil || (contact.Version != 2 && (contact.LocalName != "" || contact.PeerName != "")) {
			return ErrState
		}
		if len(contact.Chat) != 0 && (state.MessageStoreState != "pending" || validateChat(contact.Chat, contact.Local, contact.Peer) != nil) {
			return ErrState
		}
	}
	if err := validatePendingContacts(state.PendingContacts, local.PublicKey()); err != nil {
		return ErrState
	}
	if err := validatePendingContacts(state.CommittedContacts, local.PublicKey()); err != nil {
		return ErrState
	}
	for _, contact := range state.Contacts {
		if contact.Version != 2 {
			continue
		}
		found := false
		for _, record := range state.CommittedContacts {
			if record.Version == 2 && record.ID == contact.ContactID && record.Local.Equal(contact.Local) && record.Peer.Equal(contact.Peer) {
				found = true
				break
			}
		}
		if !found {
			return ErrState
		}
	}
	for _, pending := range state.PendingContacts {
		for _, committed := range state.CommittedContacts {
			if pending.ID == committed.ID || pending.Peer.Equal(committed.Peer) {
				return ErrState
			}
		}
	}
	return validateRouteStates(state)
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
