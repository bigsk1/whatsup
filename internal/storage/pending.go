package storage

import (
	"bytes"
	"encoding/base64"
	"errors"

	"tailscale.com/types/key"

	"whatsup/internal/identity"
)

const MaxPendingContacts = 32

var (
	ErrPending         = errors.New("pending contact credentials are invalid")
	ErrPendingConflict = errors.New("pending contact credentials conflict with an existing record")
)

// PendingRecord is a backend-only copy for encrypted persistence. It is not an
// approved contact, a transport capability, or a frontend status DTO. A restored
// record remains inert: neither parsing nor saving it grants chat/history access.
// The transport owns the live credentials and must supply exactly its bundle.
// These strings may have Go-managed copies; erasure cannot be guaranteed.
type PendingRecord struct {
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

func (PendingRecord) String() string   { return "[private WhatsUp pending contact]" }
func (PendingRecord) GoString() string { return "[private WhatsUp pending contact]" }
func (PendingRecord) MarshalJSON() ([]byte, error) {
	return nil, errors.New("pending contact credentials cannot be serialized outside encrypted storage")
}

// The method-free alias is used only inside the encrypted application envelope.
// Keeping it private prevents ordinary JSON serialization of PendingRecord from
// accidentally exporting the node private key and shared PSK.
type pendingRecordDisk PendingRecord

func pendingIDValid(encoded string) bool {
	if len(encoded) != 22 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	return err == nil && len(decoded) == 16 && !bytes.Equal(decoded, make([]byte, 16)) && base64.RawURLEncoding.EncodeToString(decoded) == encoded
}

// ValidatePendingRecord validates exact credential associations using the pinned
// standard node-key parser. It does not verify reciprocal consent or activate
// anything; the coordinator must have completed approval before creating it.
// The fixed field encodings bound the canonical record to less than 2 KiB.
func ValidatePendingRecord(record PendingRecord) error {
	if record.Version != 1 || !pendingIDValid(record.ID) || (record.Role != "inviter" && record.Role != "joiner") || !record.Local.Valid() || !record.Peer.Valid() || record.Local.Equal(record.Peer) || len(record.LocalNodePrivate) != len("privkey:")+64 || len(record.LocalNode) != len("nodekey:")+64 || len(record.PeerNode) != len("nodekey:")+64 || len(record.PresharedKey) != 43 {
		return ErrPending
	}
	var private key.NodePrivate
	if private.UnmarshalText([]byte(record.LocalNodePrivate)) != nil || private.IsZero() {
		return ErrPending
	}
	encodedPrivate, err := private.MarshalText()
	defer clear(encodedPrivate)
	if err != nil || string(encodedPrivate) != record.LocalNodePrivate {
		return ErrPending
	}
	var local, peer key.NodePublic
	if local.UnmarshalText([]byte(record.LocalNode)) != nil || local.IsZero() || local.String() != record.LocalNode || peer.UnmarshalText([]byte(record.PeerNode)) != nil || peer.IsZero() || peer.String() != record.PeerNode || local == peer || private.Public() != local {
		return ErrPending
	}
	psk, err := base64.RawURLEncoding.Strict().DecodeString(record.PresharedKey)
	defer clear(psk)
	if err != nil || len(psk) != 32 || bytes.Equal(psk, make([]byte, 32)) || base64.RawURLEncoding.EncodeToString(psk) != record.PresharedKey {
		return ErrPending
	}
	return nil
}

func validatePendingContacts(records []pendingRecordDisk, local identity.PublicKey) error {
	ids := make(map[string]bool, len(records))
	peers := make(map[identity.PublicKey]bool, len(records))
	for _, disk := range records {
		record := PendingRecord(disk)
		if ValidatePendingRecord(record) != nil || !record.Local.Equal(local) || ids[record.ID] || peers[record.Peer] {
			return ErrPending
		}
		ids[record.ID], peers[record.Peer] = true, true
	}
	return nil
}

// SavePendingContact preserves every other application section. An identical
// retry is idempotent; another bundle may not replace an ID or the same peer's
// pending attempt. Neither path creates a contact pin or a chat scope.
func (a *ApplicationState) SavePendingContact(record PendingRecord) error {
	if ValidatePendingRecord(record) != nil {
		return ErrPending
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return err
	}
	local, err := identity.FromSeed(a.state.Identity.Seed)
	if err != nil || !record.Local.Equal(local.PublicKey()) {
		return ErrPending
	}
	for _, committed := range a.state.CommittedContacts {
		if committed.ID == record.ID || committed.Peer.Equal(record.Peer) {
			return ErrPendingConflict
		}
	}
	for _, old := range a.state.PendingContacts {
		if old.ID == record.ID {
			if PendingRecord(old) == record {
				return nil
			}
			return ErrPendingConflict
		}
		if old.Peer.Equal(record.Peer) {
			return ErrPendingConflict
		}
	}
	if len(a.state.PendingContacts) >= MaxPendingContacts {
		return ErrCapacity
	}
	next := a.cloneLocked()
	next.PendingContacts = append(next.PendingContacts, pendingRecordDisk(record))
	return a.commitLocked(next)
}

// PendingContacts returns backend-only value copies. They are never promoted to
// approved Contact objects, and their JSON serialization remains disabled.
func (a *ApplicationState) PendingContacts() ([]PendingRecord, error) {
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return nil, err
	}
	records := make([]PendingRecord, len(a.state.PendingContacts))
	for index, record := range a.state.PendingContacts {
		records[index] = PendingRecord(record)
	}
	return records, nil
}

// RemovePendingContact is idempotent and preserves contact/chat generations.
// The coordinator must serialize cancellation with an in-flight SavePendingContact
// before removing it. This is not a persistent revocation tombstone or protection
// against a stale caller deliberately resubmitting the deleted record.
func (a *ApplicationState) RemovePendingContact(id string) error {
	if !pendingIDValid(id) {
		return ErrPending
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return err
	}
	for index, record := range a.state.PendingContacts {
		if record.ID == id {
			next := a.cloneLocked()
			copy(next.PendingContacts[index:], next.PendingContacts[index+1:])
			clear(next.PendingContacts[len(next.PendingContacts)-1:])
			next.PendingContacts = next.PendingContacts[:len(next.PendingContacts)-1]
			return a.commitLocked(next)
		}
	}
	return nil
}
