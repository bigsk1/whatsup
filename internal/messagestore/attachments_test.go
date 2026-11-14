package messagestore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"testing"
	"time"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/transfer"
)

func attachmentFixture(n uint64) attachmentstore.Entry {
	return attachmentstore.Entry{Version: 1, Revision: 1, Record: transfer.Record{Offer: transfer.Offer{ID: testID(n), Name: "synthetic-private-name.pdf", MediaType: "application/pdf", Size: 100, SHA256: sha256.Sum256([]byte("synthetic-body"))}, Direction: transfer.Incoming, State: transfer.Offered}, Units: []attachmentstore.Unit{}, CreatedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)}
}
func TestAttachmentEncryptedMetadataTamperAndPurposeBinding(t *testing.T) {
	for _, kind := range []string{"body", "id", "sequence", "contact", "swap", "purpose", "delete"} {
		t.Run(kind, func(t *testing.T) {
			db, scope, key, dir := fixture(t, Options{})
			metadata := scope.Attachments()
			a, b := attachmentFixture(1), attachmentFixture(2)
			if metadata.Create(a) != nil || metadata.Create(b) != nil {
				t.Fatal("create")
			}
			other, err := db.Scope(testID(901))
			if err != nil {
				t.Fatal(err)
			}
			if other.Attachments().Create(attachmentFixture(3)) != nil {
				t.Fatal("other")
			}
			x := external(t, dir)
			var failure error
			switch kind {
			case "body":
				_, failure = x.Exec("UPDATE attachments SET cipher=zeroblob(length(cipher)) WHERE contact_id=? AND sequence=1", scope.id)
			case "id":
				_, failure = x.Exec("UPDATE attachments SET message_id=? WHERE contact_id=? AND sequence=1", testID(77), scope.id)
			case "sequence":
				_, failure = x.Exec("UPDATE attachments SET sequence=99 WHERE contact_id=? AND sequence=1", scope.id)
			case "contact":
				_, failure = x.Exec("UPDATE attachments SET contact_id=? WHERE contact_id=? AND sequence=1", testID(902), scope.id)
			case "swap":
				var cipher []byte
				failure = x.QueryRow("SELECT cipher FROM attachments WHERE contact_id=? AND sequence=2", scope.id).Scan(&cipher)
				if failure == nil {
					_, failure = x.Exec("UPDATE attachments SET cipher=? WHERE contact_id=? AND sequence=1", cipher, scope.id)
				}
			case "purpose":
				_, failure = x.Exec("UPDATE attachments SET cipher=(SELECT cipher FROM records WHERE contact_id=? AND sequence=0) WHERE contact_id=? AND sequence=1", scope.id, scope.id)
			case "delete":
				_, failure = x.Exec("DELETE FROM attachments WHERE contact_id=? AND sequence=1", scope.id)
			}
			if failure != nil {
				t.Fatal(failure)
			}
			if _, err := metadata.Get(a.Record.Offer.ID); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("changed database accepted: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			opened, err := Open(dir, key, Options{})
			if err == nil {
				opened.Close()
				t.Fatal("changed database reopened")
			}
		})
	}
}
func TestAttachmentSQLiteFullLeavesOfferUncommitted(t *testing.T) {
	db, scope, key, dir := fixture(t, Options{})
	var pages int
	if err := db.conn.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(t.Context(), fmt.Sprintf("PRAGMA max_page_count=%d", pages)); err != nil {
		t.Fatal(err)
	}
	var failed bool
	for i := uint64(1); i < 100; i++ {
		err := scope.Attachments().Create(attachmentFixture(i))
		if err != nil {
			if !errors.Is(err, transfer.ErrCapacity) {
				t.Fatalf("full database not capacity: %v", err)
			}
			failed = true
			break
		}
	}
	if !failed {
		t.Fatal("SQLite page-full fault did not fire")
	}
	db, scope = reopen(t, db, key, dir, Options{})
	items, _, _, err := scope.Attachments().Page(0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range items {
		if e.Record.State != transfer.Offered || e.ObjectID != "" {
			t.Fatal("page full granted receiver approval")
		}
	}
}
func TestAttachmentMetadataIndependentQuotaFlagsAndHiddenPaging(t *testing.T) {
	db, scope, key, dir := fixture(t, Options{})
	other, err := db.Scope(testID(901))
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.Attachments().SetAutoAcceptImages(true); err != nil {
		t.Fatal(err)
	}
	if value, err := other.Attachments().AutoAcceptImages(); err != nil || value {
		t.Fatal("approval leaked to another contact")
	}
	for i := uint64(1); i <= 105; i++ {
		if err := scope.Attachments().Create(attachmentFixture(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := scope.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	entries, next, more, err := scope.Attachments().Page(0, 32)
	if err != nil || len(entries) != 32 || next == 0 || !more {
		t.Fatal("hidden page cursor failed")
	}
	for _, e := range entries {
		if !e.Hidden || e.Record.State != transfer.Declined {
			t.Fatal("clear did not decline unapproved offers")
		}
	}
	if err := scope.Attachments().Create(attachmentFixture(106)); err != nil {
		t.Fatal(err)
	}
	db, scope = reopen(t, db, key, dir, Options{})
	value, err := scope.Attachments().AutoAcceptImages()
	if err != nil || !value {
		t.Fatal("encrypted flag lost on restart")
	}
	entries, _, _, err = scope.Attachments().Page(0, 1)
	if err != nil || len(entries) != 1 || entries[0].Hidden || entries[0].Record.Offer.ID != testID(106) {
		t.Fatal("new offer lost after clear")
	}
	if _, err := scope.Attachments().Change(testID(106), func(e *attachmentstore.Entry) error { e.Record.Offer.Name = "substituted.pdf"; return nil }); !errors.Is(err, transfer.ErrIntegrity) {
		t.Fatal("immutable offer mutated")
	}
	if _, err := db.conn.ExecContext(context.Background(), "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		t.Fatal(err)
	}
}

func TestMaximumAttachmentIndexFitsEncryptedSQLiteRecord(t *testing.T) {
	_, scope, _, _ := fixture(t, Options{})
	entry := attachmentFixture(12)
	entry.Record.Direction = transfer.Outgoing
	entry.Record.State = transfer.Queued
	entry.Record.Offer.Size = transfer.MaxFileBytes
	entry.Ready = true
	entry.ObjectID = testID(13)
	entry.Key = make([]byte, 32)
	for i := 0; i < attachmentstore.MaxUnits; i++ {
		unit := attachmentstore.Unit{Offset: entry.CipherBytes, PlainBytes: attachmentstore.UnitBytes, CipherBytes: attachmentstore.UnitBytes + 72}
		entry.Units = append(entry.Units, unit)
		entry.CipherBytes += uint64(unit.CipherBytes)
	}
	if err := scope.Attachments().Create(entry); err != nil {
		t.Fatal("2GiB unit index exceeded the encrypted record bound", err)
	}
	loaded, err := scope.Attachments().Get(entry.Record.Offer.ID)
	if err != nil || len(loaded.Units) != 2048 || loaded.PlainBytes() != transfer.MaxFileBytes {
		t.Fatal("maximum unit index did not round-trip")
	}
}
