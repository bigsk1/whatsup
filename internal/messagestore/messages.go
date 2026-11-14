package messagestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
	"unicode/utf8"

	"whatsup/internal/message"
)

var _ message.Repository = (*Scope)(nil)

var minimumRecordCharge = func() uint64 {
	r := record{Version: 1, Message: message.Message{ID: "AAAAAAAAAAAAAAAAAAAAAA", Direction: message.Incoming, Text: "x", CreatedAt: time.Unix(0, 0).UTC(), Delivered: true}, Digest: digest("x")}
	encoded, _ := json.Marshal(r)
	return uint64(len(encoded)) + 24 + 16 + RowOverhead
}()

func validMessage(m message.Message) bool {
	return validID(m.ID) && !m.CreatedAt.IsZero() && len(m.Text) > 0 && len(m.Text) <= MaxTextBytes && utf8.ValidString(m.Text)
}

func (s *Scope) insert(ctx context.Context, tx *sql.Tx, m *metadata, metaID string, r record) error {
	if m.NextSequence >= math.MaxInt64 {
		return ErrCapacity
	}
	sequence := m.NextSequence
	blob, err := s.db.seal("message", s.id, r.Message.ID, sequence, r)
	if err != nil {
		return err
	}
	cost := uint64(len(blob)) + RowOverhead
	if cost > s.db.options.QuotaBytes || m.UsedBytes > s.db.options.QuotaBytes-cost {
		return ErrCapacity
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO records(contact_id,message_id,sequence,cipher) VALUES(?,?,?,?)", s.id, r.Message.ID, sequence, blob); err != nil {
		return sqlError(err)
	}
	m.NextSequence++
	m.Rows++
	m.UsedBytes += cost
	if r.Pending {
		m.Pending++
	}
	if err = s.db.saveMeta(ctx, tx, s.id, metaID, *m); err != nil {
		return err
	}
	if r.Pending {
		s.db.afterCommit = append(s.db.afterCommit, func() { s.db.contacts[s.id].pending[r.Message.ID] = sequence })
	}
	return nil
}

func (s *Scope) Queue(input message.Message) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	if !validMessage(input) || input.Direction != message.Outgoing || input.Delivered {
		return ErrCorrupt
	}
	input.CreatedAt = input.CreatedAt.UTC()
	input.Sequence = 0
	return s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, key, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		old, _, err := s.load(ctx, tx, input.ID)
		if err == nil {
			if old.Message.Direction == message.Outgoing && old.Pending && !old.Hidden && old.Message.Text == input.Text && old.Message.CreatedAt.Equal(input.CreatedAt) {
				return nil
			}
			return ErrConflict
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		return s.insert(ctx, tx, &m, key, record{Version: 1, Message: input, Pending: true, Digest: digest(input.Text)})
	})
}

func (s *Scope) Receive(input message.Message) (duplicate bool, result error) {
	if s == nil || s.db == nil {
		return false, ErrClosed
	}
	if !validMessage(input) {
		return false, ErrCorrupt
	}
	input.Direction = message.Incoming
	input.Delivered = true
	input.CreatedAt = input.CreatedAt.UTC()
	input.Sequence = 0
	result = s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, key, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		old, _, err := s.load(ctx, tx, input.ID)
		if err == nil {
			if old.Message.Direction != message.Incoming || old.Digest != digest(input.Text) {
				return ErrConflict
			}
			duplicate = true
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		return s.insert(ctx, tx, &m, key, record{Version: 1, Message: input, Digest: digest(input.Text)})
	})
	if result != nil {
		return false, result
	}
	return duplicate, result
}

func (s *Scope) replace(ctx context.Context, tx *sql.Tx, m *metadata, metaID string, r record, old []byte) error {
	blob, err := s.db.seal("message", s.id, r.Message.ID, r.Message.Sequence, r)
	if err != nil {
		return err
	}
	if uint64(len(old)) > m.UsedBytes {
		return ErrCorrupt
	}
	used := m.UsedBytes - uint64(len(old)) + uint64(len(blob))
	if used > s.db.options.QuotaBytes && len(blob) > len(old) {
		return ErrCapacity
	}
	result, err := tx.ExecContext(ctx, "UPDATE records SET cipher=? WHERE contact_id=? AND message_id=? AND sequence=?", blob, s.id, r.Message.ID, r.Message.Sequence)
	if err != nil {
		return sqlError(err)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrCorrupt
	}
	m.UsedBytes = used
	return s.db.saveMeta(ctx, tx, s.id, metaID, *m)
}

func (s *Scope) Acknowledge(id string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	return s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, key, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		r, old, err := s.load(ctx, tx, id)
		if errors.Is(err, ErrNotFound) {
			return ErrAck
		}
		if err != nil {
			return err
		}
		if r.Message.Direction != message.Outgoing {
			return ErrAck
		}
		if !r.Pending {
			return nil
		}
		if m.Pending == 0 {
			return ErrCorrupt
		}
		r.Pending = false
		r.Message.Delivered = true
		m.Pending--
		if r.Hidden {
			r.Message.Text = ""
		}
		if err = s.replace(ctx, tx, &m, key, r, old); err != nil {
			return err
		}
		s.db.afterCommit = append(s.db.afterCommit, func() { delete(s.db.contacts[s.id].pending, id) })
		return nil
	})
}

func (s *Scope) Get(id string) (result message.Message, err error) {
	if s == nil || s.db == nil {
		return result, ErrClosed
	}
	err = s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		if _, _, err := s.meta(ctx, tx); err != nil {
			return err
		}
		r, _, err := s.load(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Hidden && !r.Pending {
			return ErrNotFound
		}
		result = r.Message
		return nil
	})
	if err != nil {
		return message.Message{}, err
	}
	return result, err
}

func (s *Scope) PendingIDs() (ids []string, result error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	result = s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, _, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		pending := s.db.contacts[s.id].pending
		if uint64(len(pending)) != m.Pending {
			return ErrCorrupt
		}
		ids = make([]string, 0, len(pending))
		for id := range pending {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return pending[ids[i]] < pending[ids[j]] })
		return nil
	})
	if result != nil {
		return nil, result
	}
	return ids, result
}

func (s *Scope) Page(beforeSequence uint64, limit int) (messages []message.Message, result error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	if limit < 1 || limit > MaxPageMessages {
		return nil, ErrCapacity
	}
	if beforeSequence == 0 {
		beforeSequence = math.MaxInt64
	}
	if beforeSequence > math.MaxInt64 {
		return nil, ErrCorrupt
	}
	result = s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		if _, _, err := s.meta(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT message_id,sequence,cipher FROM records WHERE contact_id=? AND sequence>0 AND sequence<? ORDER BY sequence DESC", s.id, beforeSequence)
		if err != nil {
			return sqlError(err)
		}
		defer rows.Close()
		messages = make([]message.Message, 0, limit)
		used := 0
		for rows.Next() {
			var id string
			var seq uint64
			var blob []byte
			if rows.Scan(&id, &seq, &blob) != nil {
				return ErrStore
			}
			var r record
			if s.db.open("message", s.id, id, seq, blob, &r) != nil || validateRecord(id, r) != nil {
				return ErrCorrupt
			}
			if r.Hidden {
				continue
			}
			if used+len(r.Message.Text) > MaxPageTextBytes {
				break
			}
			r.Message.Sequence = seq
			messages = append(messages, r.Message)
			used += len(r.Message.Text)
			if len(messages) == limit {
				break
			}
		}
		if err = rows.Err(); err != nil {
			return sqlError(err)
		}
		for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
			messages[i], messages[j] = messages[j], messages[i]
		}
		return nil
	})
	if result != nil {
		return nil, result
	}
	return messages, result
}

func (s *Scope) Usage() (usage Usage, result error) {
	if s == nil || s.db == nil {
		return usage, ErrClosed
	}
	result = s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, _, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		quota := s.db.options.QuotaBytes
		usage = Usage{UsedBytes: m.UsedBytes, QuotaBytes: quota, Full: m.UsedBytes >= quota || quota-m.UsedBytes < minimumRecordCharge}
		return nil
	})
	return usage, result
}

func (s *Scope) DeleteLocal(id string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	return s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, key, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		r, old, err := s.load(ctx, tx, id)
		if err != nil {
			return err
		}
		if r.Hidden {
			return nil
		}
		r.Hidden = true
		if !r.Pending {
			r.Message.Text = ""
		}
		return s.replace(ctx, tx, &m, key, r, old)
	})
}

func (s *Scope) ClearHistory() error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	return s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, key, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		var after uint64
		for {
			rows, err := tx.QueryContext(ctx, "SELECT message_id,sequence,cipher FROM records WHERE contact_id=? AND sequence>? ORDER BY sequence LIMIT 128", s.id, after)
			if err != nil {
				return sqlError(err)
			}
			type change struct {
				r   record
				old []byte
			}
			batch := make([]change, 0, 128)
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
				r.Message.Sequence = seq
				batch = append(batch, change{r, blob})
				after = seq
			}
			if err = rows.Err(); err != nil {
				rows.Close()
				return sqlError(err)
			}
			rows.Close()
			if len(batch) == 0 {
				break
			}
			for _, item := range batch {
				if item.r.Hidden {
					continue
				}
				item.r.Hidden = true
				if !item.r.Pending {
					item.r.Message.Text = ""
				}
				if err = s.replace(ctx, tx, &m, key, item.r, item.old); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ImportLegacy is atomic and idempotent only for the exact ordered snapshot.
// It never invents delivery or retry state. Old retry deadlines are discarded.
func (d *DB) ImportLegacy(contact string, messages []message.Message) error {
	scope, err := d.Scope(contact)
	if err != nil {
		return err
	}
	return d.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, key, err := scope.meta(ctx, tx)
		if err != nil {
			return err
		}
		if m.Rows != 0 {
			if m.Rows != uint64(len(messages)) {
				return ErrCorrupt
			}
			for index, input := range messages {
				r, _, err := scope.load(ctx, tx, input.ID)
				if err != nil || r.Hidden || r.Message.Sequence != uint64(index+1) || r.Message.Direction != input.Direction || r.Message.Text != input.Text || !r.Message.CreatedAt.Equal(input.CreatedAt) || r.Message.Delivered != input.Delivered {
					return ErrCorrupt
				}
			}
			return nil
		}
		seen := make(map[string]bool, len(messages))
		for _, input := range messages {
			if !validMessage(input) || seen[input.ID] || input.Direction != message.Incoming && input.Direction != message.Outgoing || input.Direction == message.Incoming && !input.Delivered {
				return ErrCorrupt
			}
			seen[input.ID] = true
			input.CreatedAt = input.CreatedAt.UTC()
			input.Sequence = 0
			r := record{Version: 1, Message: input, Pending: input.Direction == message.Outgoing && !input.Delivered, Digest: digest(input.Text)}
			if err = scope.insert(ctx, tx, &m, key, r); err != nil {
				return err
			}
		}
		return nil
	})
}

func (d *DB) DropContact(id string) error {
	if !validContactID(id) {
		return ErrCorrupt
	}
	return d.transaction(func(ctx context.Context, tx *sql.Tx) error {
		if d.contacts[id] == nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM records WHERE contact_id=?", id); err != nil {
			return sqlError(err)
		}
		next := d.manifest
		next.Contacts = make([]string, 0, len(d.manifest.Contacts)-1)
		for _, contact := range d.manifest.Contacts {
			if contact != id {
				next.Contacts = append(next.Contacts, contact)
			}
		}
		blob, err := d.seal("manifest", "", "", 0, next)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE store SET cipher=?", blob); err != nil {
			return sqlError(err)
		}
		d.afterCommit = append(d.afterCommit, func() { d.manifest = next; delete(d.contacts, id) })
		return nil
	})
}
