package storage

import (
	"context"
	"crypto/sha256"
	"encoding/base64"

	"whatsup/internal/media"
)

// PeerAvatarSession is a backend-only lease for one exact fresh mutually
// authenticated TLS session and approved route. Its first snapshot may replace
// an older or newer persisted counter: restoring a legitimate backup must not
// prevent the owner withdrawing a picture. TLS prevents replaying old session
// records; subsequent application revisions are monotonic within this lease.
type PeerAvatarSession struct{ state *peerAvatarSessionState }
type peerAvatarSessionState struct {
	application *ApplicationState
	proof       RouteSessionProof
	ctx         context.Context
	cancel      context.CancelFunc
	id          string
	retired     bool
	seen        bool
	revision    uint64
	digest      [32]byte
}

func (PeerAvatarSession) String() string               { return "[private authenticated avatar session]" }
func (PeerAvatarSession) GoString() string             { return "[private authenticated avatar session]" }
func (PeerAvatarSession) MarshalJSON() ([]byte, error) { return nil, ErrScope }

func (a *ApplicationState) OpenPeerAvatarSession(ctx context.Context, proof RouteSessionProof) (*PeerAvatarSession, error) {
	if a == nil || a.vault == nil || ctx == nil || ctx.Err() != nil || proof.state == nil || proof.state.session == nil || proof.state.candidate.state == nil || proof.state.candidate.state.application != a || !proof.state.session.Capabilities().Avatars || proof.Validate() != nil {
		return nil, ErrContact
	}
	// The claim lives in shared TLS state, outside the vault lock. A copied
	// Session wrapper or a closed/reopened lease cannot reset first-snapshot
	// authority, including after another real TLS session has superseded it.
	if proof.state.session.ClaimAvatarScope() != nil {
		return nil, ErrScope
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	base, _, _, err := a.proofLocked(proof)
	if err != nil || ctx.Err() != nil {
		return nil, ErrContact
	}
	if _, _, err := a.nameContactLocked(base.ID); err != nil {
		return nil, err
	}
	if a.avatarSessions == nil {
		a.avatarSessions = map[string]*peerAvatarSessionState{}
	}
	if a.avatarSessions[base.ID] == nil && len(a.avatarSessions) >= MaxContacts {
		return nil, ErrCapacity
	}
	a.retireAvatarSessionLocked(base.ID)
	life, cancel := context.WithCancel(ctx)
	state := &peerAvatarSessionState{application: a, proof: proof, ctx: life, cancel: cancel, id: base.ID}
	a.avatarSessions[base.ID] = state
	return &PeerAvatarSession{state}, nil
}

func (a *ApplicationState) retireAvatarSessionLocked(id string) {
	if state := a.avatarSessions[id]; state != nil {
		state.retired = true
		state.cancel()
		delete(a.avatarSessions, id)
	}
}

func (lease *PeerAvatarSession) Close() {
	if lease == nil || lease.state == nil {
		return
	}
	s := lease.state
	a := s.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	s.retired = true
	s.cancel()
	if a.avatarSessions[s.id] == s {
		delete(a.avatarSessions, s.id)
	}
}

func (s *peerAvatarSessionState) checkLocked(revision uint64, digest [32]byte) (int, bool, error) {
	a := s.application
	if s.retired || s.ctx.Err() != nil || a.avatarSessions[s.id] != s {
		return -1, false, ErrScope
	}
	base, _, _, err := a.proofLocked(s.proof)
	if err != nil {
		return -1, false, ErrContact
	}
	index, record, err := a.nameContactLocked(s.id)
	if err != nil || record != base {
		return -1, false, ErrContact
	}
	if s.seen && revision < s.revision {
		return index, true, nil
	}
	if s.seen && revision == s.revision {
		if digest != s.digest {
			return -1, false, ErrState
		}
		return index, true, nil
	}
	return index, false, nil
}

func (lease *PeerAvatarSession) Apply(revision uint64, data []byte) (bool, error) {
	if lease == nil || lease.state == nil || revision == 0 || revision > maxAvatarRevision {
		return false, ErrState
	}
	if len(data) > 0 {
		if _, err := media.AvatarType(data); err != nil {
			return false, err
		}
	}
	s := lease.state
	a := s.application
	if s.ctx.Err() != nil || s.proof.Validate() != nil {
		return false, ErrContact
	}
	digest := sha256.Sum256(data)
	encoded := base64.StdEncoding.EncodeToString(data)
	a.vault.mu.Lock()
	_, done, err := s.checkLocked(revision, digest)
	a.vault.mu.Unlock()
	if err != nil || done {
		return false, err
	}
	// Fixed 512-square decoding is independent of the large local-image gate.
	// Recheck the exact lease, context, pin and route after decoding, under the
	// same vault guard as the durable replacement, so an old worker cannot win
	// after another authenticated session supersedes it or Forget claims it.
	if len(data) > 0 {
		if err := media.ValidateAvatar(s.ctx, data); err != nil {
			return false, err
		}
	}
	if s.ctx.Err() != nil || s.proof.Validate() != nil {
		return false, ErrContact
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	index, done, err := s.checkLocked(revision, digest)
	if err != nil || done {
		return false, err
	}
	current := a.state.Contacts[index]
	changed := current.PeerAvatar != encoded || current.PeerAvatarRevision != revision
	if changed {
		next := a.cloneLocked()
		next.Contacts[index].PeerAvatar, next.Contacts[index].PeerAvatarRevision = encoded, revision
		if err := a.commitLocked(next); err != nil {
			return false, err
		}
	}
	s.seen, s.revision, s.digest = true, revision, digest
	return changed, nil
}
