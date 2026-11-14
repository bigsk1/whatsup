package attachmentstore

import (
	"os"
	"path/filepath"
	"whatsup/internal/transfer"
)

// Remove hides a local card and permanently withdraws unfinished work. Terminal
// encrypted metadata retains the offer ID/hash so a replay cannot resurrect it.
func (s *Scope) Remove(id string) error {
	if s == nil || s.scopeState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	_, err := s.meta.Change(id, func(e *Entry) error {
		e.Hidden = true
		if e.Record.State != transfer.Complete {
			if e.Record.State != transfer.Declined && e.Record.State != transfer.Draft {
				e.Record.PendingWithdrawal = true
			}
			e.Record.State = transfer.Declined
			e.Approved = false
			e.ImageOnly = false
		}
		if e.Record.State == transfer.Declined || e.Record.State == transfer.Complete {
			e.Garbage = e.ObjectID != ""
		}
		return nil
	})
	s.cancelReadersLocked(id)
	if p := s.pending[id]; p != nil {
		clear(p.data)
		delete(s.pending, id)
	}
	if err != nil {
		return err
	}
	return s.collectLocked(id)
}

// CollectGarbage revokes preview readers and reclaims only objects whose
// encrypted metadata was already hidden/declined in the history transaction.
func (s *Scope) CollectGarbage() error {
	if s == nil || s.scopeState == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.usableLocked(); err != nil {
		return err
	}
	for r := range s.readers {
		r.cancel()
	}
	for id, p := range s.pending {
		clear(p.data)
		delete(s.pending, id)
	}
	var cursor uint64
	for {
		entries, next, more, err := s.meta.Page(cursor, 32)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Garbage {
				if err := s.collectLocked(e.Record.Offer.ID); err != nil {
					return err
				}
			}
		}
		if !more {
			return nil
		}
		cursor = next
	}
}

func (s *Scope) recover() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var cursor uint64
	for {
		entries, next, more, err := s.meta.Page(cursor, 32)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Record.State == transfer.Draft && !e.Ready || e.Hidden && (e.Active() || e.Record.State == transfer.Draft || e.Record.State == transfer.Offered) {
				_, err = s.meta.Change(e.Record.Offer.ID, func(e *Entry) error {
					if e.Record.State != transfer.Draft {
						e.Record.PendingWithdrawal = true
					}
					e.Record.State = transfer.Declined
					e.Hidden = true
					e.Approved, e.ImageOnly = false, false
					e.Garbage = e.ObjectID != ""
					return nil
				})
				if err != nil {
					return err
				}
				e.Garbage = e.ObjectID != ""
			}
			if e.Garbage {
				if err := s.collectLocked(e.Record.Offer.ID); err != nil {
					return err
				}
			}
		}
		if !more {
			return nil
		}
		cursor = next
	}
}

// DropScope runs before its SQLite scope is deleted. The encrypted root's
// deletion journal keeps the object IDs recoverable until all removals sync.
func (s *Store) DropScope(id string, meta Metadata) error {
	if s == nil || s.storeState == nil || !transfer.ValidID(id) || meta == nil {
		return ErrUnavailable
	}
	s.RetireScope(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || private(s.directory, true) != nil {
		return ErrUnavailable
	}
	var cursor uint64
	for {
		entries, next, more, err := meta.Page(cursor, 32)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.ObjectID == "" {
				continue
			}
			path := filepath.Join(s.directory, e.ObjectID+".bin")
			if _, err = os.Lstat(path); os.IsNotExist(err) {
				continue
			} else if err != nil || private(path, false) != nil {
				return ErrUnsafePath
			}
			if os.Remove(path) != nil {
				return ErrUnavailable
			}
		}
		if !more {
			break
		}
		cursor = next
	}
	return syncDirectory(s.directory)
}
