package storage

import (
	"context"
	"encoding/base64"
	"math"

	"whatsup/internal/identity"
	"whatsup/internal/media"
	"whatsup/internal/profile"
)

const maxAvatarRevision = uint64(1<<53 - 1)

func legacyAvatarEnvelope(state applicationEnvelope) bool {
	p := state.Profile
	if p.Avatar != "" || p.ShareAvatar || p.HideContactPhotos || p.AvatarRevision != 0 {
		return false
	}
	for _, c := range state.Contacts {
		if c.PeerAvatar != "" || c.PeerAvatarRevision != 0 || c.Paused {
			return false
		}
	}
	return true
}

func validateAvatarEncoded(encoded string) error {
	if encoded == "" {
		return nil
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(media.MaxAvatarBytes) {
		return ErrState
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(data) != encoded {
		return ErrState
	}
	_, err = media.AvatarType(data)
	return err
}

// SetAvatar receives only sanitized Go image-pipeline output; bytes remain in
// the encrypted envelope alongside profile names, never a readable SQLite column.
func (a *ApplicationState) SetAvatar(ctx context.Context, data []byte) (profile.Local, error) {
	if len(data) != 0 {
		if err := media.ValidateAvatar(ctx, data); err != nil {
			return profile.Local{}, err
		}
	}
	if a == nil || a.vault == nil {
		return profile.Local{}, ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return profile.Local{}, err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	if a.state.Profile.Avatar == encoded {
		return a.state.Profile, nil
	}
	if a.state.Profile.Revision == math.MaxUint64 || a.state.Profile.AvatarRevision >= maxAvatarRevision {
		return profile.Local{}, ErrCapacity
	}
	next := a.cloneLocked()
	next.Profile.Avatar = encoded
	next.Profile.Revision++
	next.Profile.AvatarRevision++
	if err := a.commitLocked(next); err != nil {
		return profile.Local{}, err
	}
	return next.Profile, nil
}

func (a *ApplicationState) SetAvatarPreferences(share, show bool) (profile.Local, error) {
	if a == nil || a.vault == nil {
		return profile.Local{}, ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return profile.Local{}, err
	}
	p := a.state.Profile
	if p.ShareAvatar == share && p.HideContactPhotos == !show {
		return p, nil
	}
	if p.Revision == math.MaxUint64 || p.AvatarRevision >= maxAvatarRevision {
		return profile.Local{}, ErrCapacity
	}
	next := a.cloneLocked()
	next.Profile.ShareAvatar, next.Profile.HideContactPhotos = share, !show
	next.Profile.Revision++
	// A preference change carries the same image with a fresh authenticated
	// revision, including an explicit empty value when sharing is turned off.
	next.Profile.AvatarRevision++
	if err := a.commitLocked(next); err != nil {
		return profile.Local{}, err
	}
	return next.Profile, nil
}

type ContactAvatar struct {
	Data     string
	Revision uint64
}

func (ContactAvatar) String() string   { return "[private WhatsUp contact photo]" }
func (ContactAvatar) GoString() string { return "[private WhatsUp contact photo]" }

func (a *ApplicationState) ContactAvatar(id string) (ContactAvatar, error) {
	if a == nil || a.vault == nil {
		return ContactAvatar{}, ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	index, _, err := a.nameContactLocked(id)
	if err != nil {
		return ContactAvatar{}, err
	}
	c := a.state.Contacts[index]
	return ContactAvatar{c.PeerAvatar, c.PeerAvatarRevision}, nil
}

// SetPeerAvatar is a strict legacy metadata writer used by offline fixtures.
// Production network exchange uses an exact PeerAvatarSession lease instead;
// this entry point refuses to bypass an active lease. Its legacy durable floor
// intentionally cannot implement fresh-session backup-restore semantics.
func (a *ApplicationState) SetPeerAvatar(ctx context.Context, id string, peer identity.PublicKey, revision uint64, data []byte) error {
	if revision == 0 || revision > maxAvatarRevision {
		return ErrState
	}
	if len(data) != 0 {
		if _, err := media.AvatarType(data); err != nil {
			return err
		}
	}
	if a == nil || a.vault == nil {
		return ErrLocked
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	// Check replay state before decoding pixels. Do not hold the vault guard
	// while waiting for the shared image decoder; unrelated chat commits must
	// remain possible during a local large-image operation.
	check := func() (int, bool, error) {
		if a.avatarSessions[id] != nil {
			return -1, false, ErrScope
		}
		index, record, err := a.nameContactLocked(id)
		if err != nil {
			return -1, false, err
		}
		if !record.Peer.Equal(peer) {
			return -1, false, ErrContact
		}
		current := a.state.Contacts[index]
		if revision < current.PeerAvatarRevision {
			return index, true, nil
		}
		if revision == current.PeerAvatarRevision {
			if current.PeerAvatar != encoded {
				return -1, false, ErrState
			}
			return index, true, nil
		}
		return index, false, nil
	}
	a.vault.mu.Lock()
	_, done, err := check()
	a.vault.mu.Unlock()
	if err != nil || done {
		return err
	}
	if len(data) != 0 {
		if err := media.ValidateAvatar(ctx, data); err != nil {
			return err
		}
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	index, done, err := check()
	if err != nil || done {
		return err
	}
	next := a.cloneLocked()
	next.Contacts[index].PeerAvatar, next.Contacts[index].PeerAvatarRevision = encoded, revision
	return a.commitLocked(next)
}
