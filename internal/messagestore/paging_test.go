package messagestore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"whatsup/internal/message"
)

func unread(t *testing.T, scope *Scope, want uint64) {
	t.Helper()
	got, err := scope.UnreadCount()
	if err != nil || got != want {
		t.Fatalf("unread count = %d, want %d, error %v", got, want, err)
	}
}

func TestUnreadIsAtomicDurableAndContactScoped(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	other, err := db.Scope(testID(901))
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 4; i++ {
		if _, err := s.Receive(testMessage(i, "synthetic unread body")); err != nil {
			t.Fatal(err)
		}
	}
	if duplicate, err := s.Receive(testMessage(1, "synthetic unread body")); err != nil || !duplicate {
		t.Fatal("retry did not preserve duplicate state")
	}
	if _, err := other.Receive(testMessage(50, "independent contact unread")); err != nil {
		t.Fatal(err)
	}
	unread(t, s, 4)
	unread(t, other, 1)
	if err := other.MarkRead(testID(1)); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-contact marker was accepted")
	}
	if err := s.MarkRead(testID(2)); err != nil {
		t.Fatal(err)
	}
	unread(t, s, 2)
	if err := s.MarkRead(testID(1)); err != nil {
		t.Fatal("older read marker was not idempotent")
	}
	if err := s.DeleteLocal(testID(3)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLocal(testID(1)); err != nil {
		t.Fatal(err)
	}
	unread(t, s, 1)
	if err := s.MarkRead(testID(3)); !errors.Is(err, ErrNotFound) {
		t.Fatal("hidden message was accepted as visible read evidence")
	}
	if err := s.Queue(testMessage(5, "local outgoing message")); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRead(testID(5)); err != nil {
		t.Fatal("visible outgoing message could not mark preceding incoming read")
	}
	unread(t, s, 0)
	if _, err := s.Receive(testMessage(6, "new incoming after read")); err != nil {
		t.Fatal(err)
	}
	db, s = reopen(t, db, key, dir, Options{})
	unread(t, s, 1)
	other, err = db.Scope(testID(901))
	if err != nil {
		t.Fatal(err)
	}
	unread(t, other, 1)
	if err := s.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	unread(t, s, 0)
	unread(t, other, 1)
	if duplicate, err := s.Receive(testMessage(6, "new incoming after read")); err != nil || !duplicate {
		t.Fatal("cleared receive retry changed deduplication")
	}
	unread(t, s, 0)
	if ids, err := s.PendingIDs(); err != nil || len(ids) != 1 || ids[0] != testID(5) {
		t.Fatal("read or clear removed pending outgoing work")
	}
	_, s = reopen(t, db, key, dir, Options{})
	unread(t, s, 0)
}

func TestReadPageAdvancesAcrossHiddenOnlyBoundedScans(t *testing.T) {
	_, s, _, _ := fixture(t, Options{})
	items := make([]message.Message, MaxPageScanRows+12)
	for i := range items {
		items[i] = testMessage(uint64(i+1), "synthetic hidden paging record")
		items[i].Direction, items[i].Delivered = message.Incoming, true
	}
	if err := s.db.ImportLegacy(s.id, items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items[1:] {
		if err := s.DeleteLocal(item.ID); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ReadPage(0, MaxPageMessages)
	if err != nil || len(first.Messages) != 0 || !first.HasMore || first.NextCursor != uint64(len(items)-MaxPageScanRows+1) {
		t.Fatalf("hidden-only page did not advance by its bounded scan: %+v, %v", first, err)
	}
	last, err := s.ReadPage(first.NextCursor, MaxPageMessages)
	if err != nil || len(last.Messages) != 1 || last.Messages[0].ID != items[0].ID || last.HasMore || last.NextCursor != 0 {
		t.Fatal("continuing a hidden-only page lost the oldest visible message")
	}
}

func TestReadPageCiphertextBudgetBoundsHiddenPendingBodies(t *testing.T) {
	_, s, _, _ := fixture(t, Options{})
	items := make([]message.Message, MaxPageScanRows)
	for i := range items {
		items[i] = testMessage(uint64(i+1), strings.Repeat("x", MaxTextBytes))
	}
	if err := s.db.ImportLegacy(s.id, items); err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if err := s.DeleteLocal(item.ID); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.ReadPage(0, MaxPageMessages)
	if err != nil || len(page.Messages) != 0 || !page.HasMore || page.NextCursor <= 1 || page.NextCursor >= uint64(len(items)+1) {
		t.Fatal("ciphertext-bounded hidden page did not advance")
	}
	if scanned := uint64(len(items)+1) - page.NextCursor; scanned >= MaxPageScanRows {
		t.Fatal("hidden pending-body scan ignored ciphertext budget")
	}
	if got, err := s.Get(items[len(items)-1].ID); err != nil || got.Text != items[len(items)-1].Text {
		t.Fatal("paging discarded hidden pending payload")
	}
}

func TestTenThousandClearBoundarySkipsHistoricalTombstones(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	items := make([]message.Message, 10001)
	for i := range items {
		items[i] = testMessage(uint64(i+1), "synthetic clear boundary history")
		items[i].Direction, items[i].Delivered = message.Incoming, true
	}
	if err := db.ImportLegacy(s.id, items); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	var firstCipher []byte
	if err := db.conn.QueryRowContext(context.Background(), "SELECT cipher FROM records WHERE contact_id=? AND sequence=1", s.id).Scan(&firstCipher); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Receive(testMessage(20000, "only visible message after large clear")); err != nil {
		t.Fatal(err)
	}
	page, err := s.ReadPage(0, MaxPageMessages)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != testID(20000) || page.HasMore {
		t.Fatal("large cleared prefix leaked into the page cursor")
	}
	unread(t, s, 1)
	if err := s.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	var unchanged []byte
	if err := db.conn.QueryRowContext(context.Background(), "SELECT cipher FROM records WHERE contact_id=? AND sequence=1", s.id).Scan(&unchanged); err != nil || !bytes.Equal(firstCipher, unchanged) {
		t.Fatal("a later clear rewrote its historical tombstone prefix")
	}
	_, s = reopen(t, db, key, dir, Options{})
	page, err = s.ReadPage(0, MaxPageMessages)
	if err != nil || len(page.Messages) != 0 || page.HasMore || page.NextCursor != 0 {
		t.Fatal("clear boundary did not survive reopening")
	}
	unread(t, s, 0)
	for _, name := range []string{Filename, Filename + "-wal", Filename + "-shm"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range []string{"synthetic clear boundary history", "only visible message after large clear", "readThroughSequence", "clearThroughSequence", "unread"} {
			if bytes.Contains(data, []byte(text)) {
				t.Fatal("message or read metadata appeared outside ciphertext")
			}
		}
	}
}

func TestReadMarkerRollsBackWithInterruptedCommit(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	if _, err := s.Receive(testMessage(1, "synthetic interrupted read marker")); err != nil {
		t.Fatal(err)
	}
	db.options.BeforeCommit = func() error { return errors.New("synthetic interrupted commit") }
	if err := s.MarkRead(testID(1)); !errors.Is(err, ErrStore) {
		t.Fatal("uncertain read marker was reported successful")
	}
	_, s = reopen(t, db, key, dir, Options{})
	unread(t, s, 1)
	if err := s.MarkRead(testID(1)); err != nil {
		t.Fatal(err)
	}
	unread(t, s, 0)
}

func TestPhaseOneMetadataUpgradesAuthenticatedAndStartsRead(t *testing.T) {
	db, s, key, dir := fixture(t, Options{})
	if _, err := s.Receive(testMessage(1, "existing phase one incoming")); err != nil {
		t.Fatal(err)
	}
	if err := s.Queue(testMessage(2, "existing phase one outbox")); err != nil {
		t.Fatal(err)
	}
	var oldRecord []byte
	if err := db.conn.QueryRowContext(context.Background(), "SELECT cipher FROM records WHERE contact_id=? AND sequence=1", s.id).Scan(&oldRecord); err != nil {
		t.Fatal(err)
	}
	if err := db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, id, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		old := metadataV1{Version: 1, Epoch: m.Epoch, NextSequence: m.NextSequence, Rows: m.Rows, UsedBytes: m.UsedBytes, Pending: m.Pending}
		blob, err := db.seal("metadata", s.id, id, 0, old)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE records SET cipher=? WHERE contact_id=? AND sequence=0", blob, s.id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	db, s = reopen(t, db, key, dir, Options{})
	unread(t, s, 0)
	if err := db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, _, err := s.meta(ctx, tx)
		if err == nil && (m.Version != 2 || m.ReadThroughSequence != 2 || m.Unread != 0) {
			return errors.New("metadata was not upgraded to a read baseline")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var unchanged []byte
	if err := db.conn.QueryRowContext(context.Background(), "SELECT cipher FROM records WHERE contact_id=? AND sequence=1", s.id).Scan(&unchanged); err != nil || !bytes.Equal(oldRecord, unchanged) {
		t.Fatal("metadata migration rewrote message ciphertext")
	}
	if ids, err := s.PendingIDs(); err != nil || len(ids) != 1 || ids[0] != testID(2) {
		t.Fatal("metadata migration changed the outbox")
	}
	if _, err := s.Receive(testMessage(3, "new phase two unread")); err != nil {
		t.Fatal(err)
	}
	unread(t, s, 1)
}

func TestReadMetadataTamperingAndImpossibleCountersFailClosed(t *testing.T) {
	for _, change := range []string{"cipher", "unread", "read-through", "clear-through", "visible-clear"} {
		t.Run(change, func(t *testing.T) {
			db, s, key, dir := fixture(t, Options{})
			if _, err := s.Receive(testMessage(1, "synthetic metadata integrity")); err != nil {
				t.Fatal(err)
			}
			if err := db.transaction(func(ctx context.Context, tx *sql.Tx) error {
				m, id, err := s.meta(ctx, tx)
				if err != nil {
					return err
				}
				switch change {
				case "unread":
					m.Unread = 0
				case "read-through":
					m.ReadThroughSequence = m.NextSequence
				case "clear-through":
					m.ClearThroughSequence = 1
				case "visible-clear":
					m.ClearThroughSequence, m.ReadThroughSequence, m.Unread = 1, 1, 0
				}
				blob, err := db.seal("metadata", s.id, id, 0, m)
				if err != nil {
					return err
				}
				if change == "cipher" {
					blob[len(blob)-1] ^= 1
				}
				_, err = tx.ExecContext(ctx, "UPDATE records SET cipher=? WHERE contact_id=? AND sequence=0", blob, s.id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if opened, err := Open(dir, key, Options{}); !errors.Is(err, ErrCorrupt) {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatal("altered read metadata was accepted")
			}
		})
	}
}
