package messagestore

import (
	"context"
	"database/sql"
)

// PeerRemoval is encrypted contact metadata and survives local history clearing.
// It pauses this association without deleting its identity pins or messages.
func (s *Scope) PeerRemoval() (id string, result error) {
	if s == nil || s.db == nil {
		return "", ErrClosed
	}
	result = s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, _, err := s.meta(ctx, tx)
		if err == nil {
			id = m.PeerRemovalID
		}
		return err
	})
	if result != nil {
		return "", result
	}
	return id, nil
}

func (s *Scope) MarkPeerRemoved(id string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	if !validContactID(id) {
		return ErrCorrupt
	}
	return s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, key, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		// Terminal and idempotent. A later receipt cannot clear or replace it.
		if m.PeerRemovalID != "" {
			return nil
		}
		m.PeerRemovalID = id
		return s.db.saveMeta(ctx, tx, s.id, key, m)
	})
}
