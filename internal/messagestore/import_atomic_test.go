package messagestore

import (
	"errors"
	"testing"

	"whatsup/internal/message"
)

func assertNoImportedRowsOrPending(t *testing.T, db *DB, s *Scope) {
	t.Helper()
	var rows int
	if err := db.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM records WHERE contact_id=? AND sequence>0", s.id).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("partial import persisted rows", err)
	}
	if len(db.contacts[s.id].pending) != 0 {
		t.Fatal("uncommitted batch published pending IDs")
	}
}

func TestLegacyBatchImportInterruptedCommitRollsBackAndRestoresPendingAfterRetry(t *testing.T) {
	armed := false
	fired := false
	var handle *DB
	db, s, key, dir := fixture(t, Options{BeforeCommit: func() error {
		// ImportLegacy first opens its Scope in a preliminary transaction.
		// Fail only when this batch has actually prepared both pending-row
		// cache callbacks, after encrypted rows and final metadata exist.
		if armed && handle != nil && len(handle.afterCommit) == 2 {
			fired = true
			return errors.New("synthetic interrupted legacy import")
		}
		return nil
	}})
	handle = db
	items := []message.Message{testMessage(21, "synthetic imported pending one"), testMessage(22, "synthetic imported pending two"), testMessage(23, "synthetic imported incoming")}
	items[2].Direction, items[2].Delivered = message.Incoming, true
	armed = true
	if err := db.ImportLegacy(s.id, items); !errors.Is(err, ErrStore) {
		t.Fatal("interrupted batch did not fail", err)
	}
	if !fired {
		t.Fatal("fault did not reach prepared batch commit")
	}
	assertNoImportedRowsOrPending(t, db, s)
	db, s = reopen(t, db, key, dir, Options{})
	assertNoImportedRowsOrPending(t, db, s)
	if page, err := s.ReadPage(0, 10); err != nil || len(page.Messages) != 0 {
		t.Fatal("interrupted batch became visible on reopen", err)
	}
	if err := db.ImportLegacy(s.id, items); err != nil {
		t.Fatal(err)
	}
	if err := db.ImportLegacy(s.id, items); err != nil {
		t.Fatal("exact retry was not idempotent", err)
	}
	db, s = reopen(t, db, key, dir, Options{})
	ids, err := s.PendingIDs()
	if err != nil || len(ids) != 2 || ids[0] != items[0].ID || ids[1] != items[1].ID {
		t.Fatal("durable outbox did not reopen with exact imported IDs", err)
	}
	for index, input := range items {
		got, err := s.Get(input.ID)
		if err != nil || got.Text != input.Text || got.Direction != input.Direction || got.Delivered != input.Delivered || got.Sequence != uint64(index+1) {
			t.Fatal("committed batch changed message or delivery state", err)
		}
	}
}

func TestLegacyBatchImportLateDuplicateAndQuotaRollbackKeepLiveScopeUsable(t *testing.T) {
	for _, quota := range []bool{false, true} {
		t.Run(map[bool]string{false: "duplicate", true: "quota"}[quota], func(t *testing.T) {
			options := Options{}
			if quota {
				options.QuotaBytes = minimumRecordCharge*3 + 64
			}
			db, s, key, dir := fixture(t, options)
			items := []message.Message{testMessage(31, "x"), testMessage(32, "x"), testMessage(33, "x"), testMessage(34, "x")}
			want := ErrCapacity
			if !quota {
				items[3].ID = items[0].ID
				want = ErrCorrupt
			}
			if err := db.ImportLegacy(s.id, items); !errors.Is(err, want) {
				t.Fatal("late failed batch did not fail atomically", err)
			}
			assertNoImportedRowsOrPending(t, db, s)
			if usage, err := s.Usage(); err != nil || usage.UsedBytes != 0 {
				t.Fatal("failed import charged storage", err)
			}
			if err := s.Queue(testMessage(35, "x")); err != nil {
				t.Fatal("rolled back import poisoned ordinary writes", err)
			}
			db, s = reopen(t, db, key, dir, options)
			ids, err := s.PendingIDs()
			if err != nil || len(ids) != 1 || ids[0] != testID(35) {
				t.Fatal("failed import leaked pending cache across reopen", err)
			}
		})
	}
}
