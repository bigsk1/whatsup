package transport

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"

	"tailscale.com/types/key"

	"whatsup/internal/pairing/channel"
	"whatsup/internal/storage"
)

var ErrPendingCredentials = errors.New("pending contact credential exchange failed")

// PendingCredentials owns the sole ongoing local node key and agreed PSK. It is
// deliberately inert, not ContactCredentials and not a constructor for transport
// authority. Copies share revocation. Only explicit encrypted persistence can
// obtain its backend record; no method exports it to JSON or the frontend.
type PendingCredentials struct{ state *pendingCredentialState }
type pendingCredentialState struct {
	mu      sync.Mutex
	record  storage.PendingRecord
	revoked bool
}

func (PendingCredentials) String() string               { return "[private pending contact credentials]" }
func (PendingCredentials) GoString() string             { return "[private pending contact credentials]" }
func (PendingCredentials) MarshalJSON() ([]byte, error) { return nil, ErrPendingCredentials }

// ExchangePending creates dedicated credentials only after bearer reservation
// and reciprocal consent. The inviter creates the one contact ID and PSK; the
// joiner creates only its own node key. Save callbacks run outside channel locks.
// Success means both pending writes were acknowledged, never contact activation.
func ExchangePending(ctx context.Context, reservation *channel.Reservation, approval *channel.Approval, persist func(storage.PendingRecord) error) (*PendingCredentials, error) {
	if ctx == nil || persist == nil || reservation == nil || approval == nil {
		return nil, ErrPendingCredentials
	}
	if err := reservation.WithLiveApproval(approval, func() error { return nil }); err != nil {
		return nil, ErrPendingCredentials
	}
	node := key.NewNode()
	defer func() { node = key.NodePrivate{} }()
	encodedPrivate, err := node.MarshalText()
	if err != nil {
		return nil, ErrPendingCredentials
	}
	defer clear(encodedPrivate)
	publicText := node.Public().String()
	if len(publicText) != len("nodekey:")+64 {
		return nil, ErrPendingCredentials
	}
	encodedPublic, err := hex.DecodeString(publicText[len("nodekey:"):])
	if err != nil || len(encodedPublic) != 32 {
		return nil, ErrPendingCredentials
	}
	var offer channel.ContactOffer
	copy(offer.Node[:], encodedPublic)
	role := "joiner"
	if approval.Role() == channel.Inviter {
		role = "inviter"
		for offer.ID == [16]byte{} {
			if _, err := rand.Read(offer.ID[:]); err != nil {
				return nil, ErrPendingCredentials
			}
		}
		for offer.PresharedKey == [32]byte{} {
			if _, err := rand.Read(offer.PresharedKey[:]); err != nil {
				return nil, ErrPendingCredentials
			}
		}
	}
	defer clear(offer.PresharedKey[:])
	var saved storage.PendingRecord
	_, err = approval.ExchangePending(ctx, reservation, offer, func(agreement channel.ContactAgreement) error {
		saved = storage.PendingRecord{
			Version: 1, ID: base64.RawURLEncoding.EncodeToString(agreement.ID[:]), Role: role,
			Local: approval.Local(), Peer: approval.Peer(), LocalNodePrivate: string(encodedPrivate),
			LocalNode:    "nodekey:" + hex.EncodeToString(agreement.LocalNode[:]),
			PeerNode:     "nodekey:" + hex.EncodeToString(agreement.PeerNode[:]),
			PresharedKey: base64.RawURLEncoding.EncodeToString(agreement.PresharedKey[:]),
		}
		if storage.ValidatePendingRecord(saved) != nil {
			return ErrPendingCredentials
		}
		return persist(saved)
	})
	if err != nil {
		saved = storage.PendingRecord{}
		return nil, ErrPendingCredentials
	}
	return &PendingCredentials{state: &pendingCredentialState{record: saved}}, nil
}

func (p *PendingCredentials) RecordForEncryptedStorage() (storage.PendingRecord, error) {
	if p == nil || p.state == nil {
		return storage.PendingRecord{}, ErrPendingCredentials
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	if p.state.revoked || storage.ValidatePendingRecord(p.state.record) != nil {
		return storage.PendingRecord{}, ErrPendingCredentials
	}
	return p.state.record, nil
}

func (p *PendingCredentials) Revoke() {
	if p == nil || p.state == nil {
		return
	}
	p.state.mu.Lock()
	defer p.state.mu.Unlock()
	p.state.revoked = true
	p.state.record = storage.PendingRecord{}
}
