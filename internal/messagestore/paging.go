package messagestore

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"whatsup/internal/message"
)

var _ message.PagedRepository = (*Scope)(nil)

// Authenticate the complete existing store in the same transaction as the
// metadata upgrade. Old history starts read; message ciphertext stays unchanged.
func (d *DB) upgradeMetadata(ctx context.Context) error {
	tx, err := d.conn.BeginTx(ctx, nil)
	if err != nil {
		return sqlError(err)
	}
	defer tx.Rollback()
	if err = d.verifyAll(ctx, tx); err != nil {
		return err
	}
	for _, contact := range d.manifest.Contacts {
		m, key, err := d.loadMeta(ctx, tx, contact)
		if err != nil {
			return err
		}
		if m.Version == 1 {
			m.Version = 2
			if err = d.saveMeta(ctx, tx, contact, key, m); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrStore, sqlError(err))
	}
	return nil
}

func (s *Scope) ReadPage(before uint64, limit int) (page message.Page, result error) {
	if s == nil || s.db == nil {
		return page, ErrClosed
	}
	if limit < 1 || limit > MaxPageMessages {
		return page, ErrCapacity
	}
	if before > math.MaxInt64 {
		return page, ErrCorrupt
	}
	result = s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, _, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		upper := before
		if upper == 0 || upper > m.NextSequence {
			upper = m.NextSequence
		}
		page.Messages = make([]message.Message, 0, limit)
		if upper <= m.ClearThroughSequence+1 {
			return nil
		}
		rows, err := tx.QueryContext(ctx, "SELECT message_id,sequence,cipher FROM records WHERE contact_id=? AND sequence>? AND sequence<? ORDER BY sequence DESC LIMIT ?", s.id, m.ClearThroughSequence, upper, MaxPageScanRows)
		if err != nil {
			return sqlError(err)
		}
		defer rows.Close()
		last := upper
		var cipherBytes, textBytes int
		for rows.Next() {
			var id string
			var seq uint64
			var blob []byte
			if rows.Scan(&id, &seq, &blob) != nil {
				return ErrStore
			}
			if cipherBytes+len(blob) > MaxPageScanBytes {
				break
			}
			var r record
			if s.db.open("message", s.id, id, seq, blob, &r) != nil || validateRecord(id, r) != nil {
				return ErrCorrupt
			}
			if !r.Hidden && textBytes+len(r.Message.Text) > MaxPageTextBytes {
				break
			}
			last = seq
			cipherBytes += len(blob)
			if r.Hidden {
				continue
			}
			r.Message.Sequence = seq
			page.Messages = append(page.Messages, r.Message)
			textBytes += len(r.Message.Text)
			if len(page.Messages) == limit {
				break
			}
		}
		if err = rows.Err(); err != nil {
			return sqlError(err)
		}
		// Record sequences are contiguous and authenticated against metadata at
		// open and after outside writes. No hidden row is dropped from storage.
		page.HasMore = last > m.ClearThroughSequence+1
		if page.HasMore {
			if last == upper {
				return ErrCorrupt
			}
			page.NextCursor = last
		}
		for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
			page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
		}
		return nil
	})
	if result != nil {
		return message.Page{}, result
	}
	return page, nil
}

func (s *Scope) UnreadCount() (count uint64, result error) {
	if s == nil || s.db == nil {
		return 0, ErrClosed
	}
	result = s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, _, err := s.meta(ctx, tx)
		if err == nil {
			count = m.Unread
		}
		return err
	})
	if result != nil {
		return 0, result
	}
	return count, nil
}

func (s *Scope) MarkRead(throughMessageID string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	return s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, key, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		through, _, err := s.load(ctx, tx, throughMessageID)
		if err != nil {
			return err
		}
		if through.Hidden || through.Message.Sequence <= m.ClearThroughSequence {
			return ErrNotFound
		}
		if through.Message.Sequence <= m.ReadThroughSequence {
			return nil
		}
		rows, err := tx.QueryContext(ctx, "SELECT message_id,sequence,cipher FROM records WHERE contact_id=? AND sequence>? AND sequence<=? ORDER BY sequence", s.id, m.ReadThroughSequence, through.Message.Sequence)
		if err != nil {
			return sqlError(err)
		}
		var read uint64
		for rows.Next() {
			var id string
			var seq uint64
			var blob []byte
			if rows.Scan(&id, &seq, &blob) != nil {
				rows.Close()
				return ErrStore
			}
			var r record
			if s.db.open("message", s.id, id, seq, blob, &r) != nil || validateRecord(id, r) != nil {
				rows.Close()
				return ErrCorrupt
			}
			if !r.Hidden && r.Message.Direction == message.Incoming {
				read++
			}
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return sqlError(err)
		}
		rows.Close()
		if read > m.Unread {
			return ErrCorrupt
		}
		m.ReadThroughSequence = through.Message.Sequence
		m.Unread -= read
		return s.db.saveMeta(ctx, tx, s.id, key, m)
	})
}
