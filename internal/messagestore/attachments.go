package messagestore

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/transfer"
)

type attachmentDisk attachmentstore.Entry

func validAttachmentMetadata(m metadata) bool {
	return m.AttachmentUsed <= attachmentstore.QuotaBytes && m.AttachmentNext <= math.MaxInt64 && (m.AttachmentRows == 0 && m.AttachmentNext == 0 && m.AttachmentUsed == 0 || m.AttachmentRows > 0 && m.AttachmentNext == m.AttachmentRows+1)
}

type AttachmentMetadata struct{ scope *Scope }

// ExistingScope never creates a row. Deletion recovery must distinguish an
// already retired database scope from an interrupted object cleanup.
func (d *DB) ExistingScope(id string) (result *Scope, err error) {
	if !validID(id) {
		return nil, ErrClosed
	}
	err = d.transaction(func(ctx context.Context, tx *sql.Tx) error {
		if cache := d.contacts[id]; cache != nil {
			result = &Scope{db: d, id: id, epoch: cache.epoch}
		}
		return nil
	})
	return
}

func (AttachmentMetadata) String() string               { return "[private attachment metadata scope]" }
func (AttachmentMetadata) GoString() string             { return "[private attachment metadata scope]" }
func (AttachmentMetadata) MarshalJSON() ([]byte, error) { return nil, ErrClosed }

var _ attachmentstore.Metadata = (*AttachmentMetadata)(nil)

func (s *Scope) Attachments() *AttachmentMetadata { return &AttachmentMetadata{scope: s} }
func (a *AttachmentMetadata) Epoch() string {
	if a == nil || a.scope == nil {
		return ""
	}
	return a.scope.epoch
}

func (a *AttachmentMetadata) load(ctx context.Context, q queryer, id string) (attachmentstore.Entry, error) {
	if !transfer.ValidID(id) {
		return attachmentstore.Entry{}, transfer.ErrUnavailable
	}
	var sequence uint64
	var blob []byte
	err := q.QueryRowContext(ctx, "SELECT sequence,cipher FROM attachments WHERE contact_id=? AND message_id=?", a.scope.id, id).Scan(&sequence, &blob)
	if errors.Is(err, sql.ErrNoRows) {
		return attachmentstore.Entry{}, transfer.ErrUnavailable
	}
	if err != nil {
		return attachmentstore.Entry{}, ErrStore
	}
	var wire attachmentDisk
	if a.scope.db.open("attachment", a.scope.id, id, sequence, blob, &wire) != nil {
		return attachmentstore.Entry{}, ErrCorrupt
	}
	e := attachmentstore.Entry(wire)
	e.Sequence = sequence
	if e.Record.Offer.ID != id || e.Validate() != nil {
		return attachmentstore.Entry{}, ErrCorrupt
	}
	return e, nil
}

func (d *DB) verifyAttachments(ctx context.Context, q queryer, contact string, m metadata, cache *contactCache) error {
	if m.AttachmentRows == 0 && m.AttachmentNext != 0 || m.AttachmentRows != 0 && m.AttachmentNext != m.AttachmentRows+1 || m.AttachmentNext > math.MaxInt64 || m.AttachmentUsed > attachmentstore.QuotaBytes {
		return ErrCorrupt
	}
	rows, err := q.QueryContext(ctx, "SELECT message_id,sequence,cipher FROM attachments WHERE contact_id=? ORDER BY sequence", contact)
	if err != nil {
		return ErrStore
	}
	defer rows.Close()
	cache.attachments = make(map[string]bool)
	var count, used uint64
	active, drafts, offered := 0, 0, 0
	for rows.Next() {
		var id string
		var sequence uint64
		var blob []byte
		if rows.Scan(&id, &sequence, &blob) != nil || sequence != count+1 {
			return ErrCorrupt
		}
		var wire attachmentDisk
		if d.open("attachment", contact, id, sequence, blob, &wire) != nil {
			return ErrCorrupt
		}
		e := attachmentstore.Entry(wire)
		if e.Record.Offer.ID != id || e.Validate() != nil {
			return ErrCorrupt
		}
		count++
		used += e.Charge()
		if e.Active() {
			active++
			cache.attachments[id] = true
		}
		if e.Record.State == "draft" {
			drafts++
			cache.attachments[id] = true
		}
		if e.Record.State == transfer.Offered {
			offered++
			cache.attachments[id] = true
		}
	}
	if rows.Err() != nil {
		return ErrStore
	}
	if count != m.AttachmentRows || used != m.AttachmentUsed || active > transfer.MaxActive || drafts > attachmentstore.MaxDrafts || offered > transfer.MaxOffers {
		return ErrCorrupt
	}
	return nil
}

func (a *AttachmentMetadata) run(op func(context.Context, *sql.Tx, *metadata, string) error) error {
	if a == nil || a.scope == nil || a.scope.db == nil {
		return ErrClosed
	}
	err := a.scope.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, key, err := a.scope.meta(ctx, tx)
		if err != nil {
			return err
		}
		return op(ctx, tx, &m, key)
	})
	if errors.Is(err, ErrCapacity) {
		return errors.Join(transfer.ErrCapacity, err)
	}
	return err
}
func (a *AttachmentMetadata) Get(id string) (result attachmentstore.Entry, err error) {
	err = a.run(func(ctx context.Context, tx *sql.Tx, _ *metadata, _ string) error {
		var e error
		result, e = a.load(ctx, tx, id)
		return e
	})
	return
}
func (a *AttachmentMetadata) Active() (result []attachmentstore.Entry, err error) {
	err = a.run(func(ctx context.Context, tx *sql.Tx, _ *metadata, _ string) error {
		for id := range a.scope.db.contacts[a.scope.id].attachments {
			entry, e := a.load(ctx, tx, id)
			if e != nil {
				return e
			}
			result = append(result, entry)
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
		return nil
	})
	return
}
func (a *AttachmentMetadata) Page(before uint64, limit int) (result []attachmentstore.Entry, next uint64, more bool, err error) {
	if limit < 1 || limit > 100 || before > math.MaxInt64 {
		return nil, 0, false, ErrCapacity
	}
	err = a.run(func(ctx context.Context, tx *sql.Tx, m *metadata, _ string) error {
		upper := before
		if upper == 0 {
			upper = m.AttachmentNext
		}
		if upper == 0 {
			return nil
		}
		rows, e := tx.QueryContext(ctx, "SELECT message_id,sequence,cipher FROM attachments WHERE contact_id=? AND sequence<? ORDER BY sequence DESC LIMIT ?", a.scope.id, upper, limit)
		if e != nil {
			return sqlError(e)
		}
		defer rows.Close()
		var last uint64
		var scannedBytes int
		for rows.Next() {
			var id string
			var sequence uint64
			var blob []byte
			if rows.Scan(&id, &sequence, &blob) != nil {
				return ErrStore
			}
			if len(result) > 0 && scannedBytes+len(blob) > MaxPageScanBytes {
				break
			}
			scannedBytes += len(blob)
			var wire attachmentDisk
			if a.scope.db.open("attachment", a.scope.id, id, sequence, blob, &wire) != nil {
				return ErrCorrupt
			}
			entry := attachmentstore.Entry(wire)
			entry.Sequence = sequence
			if entry.Record.Offer.ID != id || entry.Validate() != nil {
				return ErrCorrupt
			}
			result = append(result, entry)
			last = sequence
		}
		if rows.Err() != nil {
			return ErrStore
		}
		more = last > 1
		if more {
			next = last
		}
		return nil
	})
	return
}

func (a *AttachmentMetadata) checkLimits(ctx context.Context, tx *sql.Tx, next attachmentstore.Entry, previous *attachmentstore.Entry) error {
	active, drafts, offered := 0, 0, 0
	for id := range a.scope.db.contacts[a.scope.id].attachments {
		if id == next.Record.Offer.ID {
			continue
		}
		e, err := a.load(ctx, tx, id)
		if err != nil {
			return err
		}
		if e.Active() {
			active++
		}
		if e.Record.State == "draft" {
			drafts++
		}
		if e.Record.State == transfer.Offered {
			offered++
		}
	}
	if next.Active() {
		active++
	}
	if next.Record.State == "draft" {
		drafts++
	}
	if next.Record.State == transfer.Offered {
		offered++
	}
	if active > transfer.MaxActive || drafts > attachmentstore.MaxDrafts || offered > transfer.MaxOffers {
		return transfer.ErrCapacity
	}
	return nil
}
func (a *AttachmentMetadata) cache(entry attachmentstore.Entry) {
	d := a.scope.db
	id := entry.Record.Offer.ID
	live := entry.Active() || entry.Record.State == "draft" || entry.Record.State == transfer.Offered
	d.afterCommit = append(d.afterCommit, func() {
		cache := d.contacts[a.scope.id]
		if cache.attachments == nil {
			cache.attachments = map[string]bool{}
		}
		if live {
			cache.attachments[id] = true
		} else {
			delete(cache.attachments, id)
		}
	})
}
func (a *AttachmentMetadata) Create(entry attachmentstore.Entry) error {
	entry = attachmentstore.Clone(entry)
	entry.Version = 1
	entry.Revision = 1
	if entry.Validate() != nil {
		return transfer.ErrIntegrity
	}
	return a.run(func(ctx context.Context, tx *sql.Tx, m *metadata, key string) error {
		if _, err := a.load(ctx, tx, entry.Record.Offer.ID); err == nil {
			return transfer.ErrConflict
		} else if !errors.Is(err, transfer.ErrUnavailable) {
			return err
		}
		if err := a.checkLimits(ctx, tx, entry, nil); err != nil {
			return err
		}
		if entry.Charge() > attachmentstore.QuotaBytes-m.AttachmentUsed {
			return transfer.ErrCapacity
		}
		if m.AttachmentNext == 0 {
			m.AttachmentNext = 1
		}
		if m.AttachmentNext >= math.MaxInt64 {
			return transfer.ErrCapacity
		}
		entry.Sequence = m.AttachmentNext
		blob, err := a.scope.db.seal("attachment", a.scope.id, entry.Record.Offer.ID, entry.Sequence, attachmentDisk(entry))
		if err != nil {
			return err
		}
		if len(blob) > 120<<10 {
			return transfer.ErrCapacity
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO attachments(contact_id,message_id,sequence,cipher) VALUES(?,?,?,?)", a.scope.id, entry.Record.Offer.ID, entry.Sequence, blob); err != nil {
			return sqlError(err)
		}
		m.AttachmentNext++
		m.AttachmentRows++
		m.AttachmentUsed += entry.Charge()
		if err = a.scope.db.saveMeta(ctx, tx, a.scope.id, key, *m); err != nil {
			return err
		}
		a.cache(entry)
		return nil
	})
}
func (a *AttachmentMetadata) Change(id string, change func(*attachmentstore.Entry) error) (result attachmentstore.Entry, err error) {
	if change == nil {
		return result, transfer.ErrUnavailable
	}
	err = a.run(func(ctx context.Context, tx *sql.Tx, m *metadata, key string) error {
		old, e := a.load(ctx, tx, id)
		if e != nil {
			return e
		}
		next := attachmentstore.Clone(old)
		if e = change(&next); e != nil {
			return e
		}
		next.Revision = old.Revision + 1
		if next.Record.Offer != old.Record.Offer || next.Record.Direction != old.Record.Direction || next.Sequence != old.Sequence || next.CreatedAt != old.CreatedAt || next.Validate() != nil {
			return transfer.ErrIntegrity
		}
		if e = a.checkLimits(ctx, tx, next, &old); e != nil {
			return e
		}
		if old.Charge() > m.AttachmentUsed {
			return ErrCorrupt
		}
		used := m.AttachmentUsed - old.Charge()
		if next.Charge() > attachmentstore.QuotaBytes-used {
			return transfer.ErrCapacity
		}
		blob, e := a.scope.db.seal("attachment", a.scope.id, id, next.Sequence, attachmentDisk(next))
		if e != nil {
			return e
		}
		if len(blob) > 120<<10 {
			return transfer.ErrCapacity
		}
		if _, e = tx.ExecContext(ctx, "UPDATE attachments SET cipher=? WHERE contact_id=? AND message_id=?", blob, a.scope.id, id); e != nil {
			return sqlError(e)
		}
		m.AttachmentUsed = used + next.Charge()
		if e = a.scope.db.saveMeta(ctx, tx, a.scope.id, key, *m); e != nil {
			return e
		}
		a.cache(next)
		result = next
		return nil
	})
	return
}
func (a *AttachmentMetadata) AutoAcceptImages() (value bool, err error) {
	err = a.run(func(_ context.Context, _ *sql.Tx, m *metadata, _ string) error { value = m.AutoImages; return nil })
	return
}
func (a *AttachmentMetadata) SetAutoAcceptImages(value bool) error {
	return a.run(func(ctx context.Context, tx *sql.Tx, m *metadata, key string) error {
		m.AutoImages = value
		return a.scope.db.saveMeta(ctx, tx, a.scope.id, key, *m)
	})
}

func (s *Scope) hideAttachments(ctx context.Context, tx *sql.Tx, id string) error {
	var after uint64
	for {
		query := "SELECT message_id,sequence,cipher FROM attachments WHERE contact_id=? AND sequence>? ORDER BY sequence LIMIT 32"
		rows, err := tx.QueryContext(ctx, query, s.id, after)
		if err != nil {
			return ErrStore
		}
		var batch []attachmentstore.Entry
		for rows.Next() {
			var recordID string
			var sequence uint64
			var blob []byte
			if rows.Scan(&recordID, &sequence, &blob) != nil {
				rows.Close()
				return ErrStore
			}
			var disk attachmentDisk
			if s.db.open("attachment", s.id, recordID, sequence, blob, &disk) != nil {
				rows.Close()
				return ErrCorrupt
			}
			entry := attachmentstore.Entry(disk)
			entry.Sequence = sequence
			if entry.Validate() != nil {
				rows.Close()
				return ErrCorrupt
			}
			batch = append(batch, entry)
			after = sequence
		}
		if rows.Err() != nil {
			rows.Close()
			return ErrStore
		}
		rows.Close()
		if len(batch) == 0 {
			return nil
		}
		for _, entry := range batch {
			if id != "" && entry.Record.Offer.ID != id {
				continue
			}
			// Also retire hidden work left by older Clear history versions.
			// A hidden queued/paused transfer must never retain a slot or send.
			if entry.Hidden && !entry.Active() && entry.Record.State != transfer.Offered && entry.Record.State != transfer.Draft {
				continue
			}
			entry.Hidden = true
			entry.Revision++
			if entry.Record.State != transfer.Complete {
				entry.Record.State = transfer.Declined
				entry.Approved = false
				entry.ImageOnly = false
			}
			if entry.Record.State == transfer.Complete || entry.Record.State == transfer.Declined {
				entry.Garbage = entry.ObjectID != ""
			}
			if entry.Validate() != nil {
				return ErrCorrupt
			}
			blob, err := s.db.seal("attachment", s.id, entry.Record.Offer.ID, entry.Sequence, attachmentDisk(entry))
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE attachments SET cipher=? WHERE contact_id=? AND message_id=?", blob, s.id, entry.Record.Offer.ID); err != nil {
				return sqlError(err)
			}
			(&AttachmentMetadata{scope: s}).cache(entry)
		}
	}
}
