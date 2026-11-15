package messagestore

import (
	"database/sql"
	"testing"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/transfer"
)

func TestContactStorageUsageTracksEncryptedOutboxTombstonesAndAttachmentReservations(t *testing.T) {
	db, scope, _, _ := fixture(t, Options{})
	other, err := db.Scope(testID(901))
	if err != nil {
		t.Fatal(err)
	}
	assertUsage := func(attachmentUsed uint64) {
		t.Helper()
		usage, err := scope.StorageUsage()
		if err != nil {
			t.Fatal(err)
		}
		var messageCharge sql.NullInt64
		if err := db.conn.QueryRowContext(t.Context(), "SELECT SUM(length(cipher)+?) FROM records WHERE contact_id=? AND sequence>0", RowOverhead, scope.id).Scan(&messageCharge); err != nil {
			t.Fatal(err)
		}
		if usage.Messages.UsedBytes != uint64(messageCharge.Int64) || usage.Messages.QuotaBytes != DefaultQuotaBytes || usage.Attachments.UsedBytes != attachmentUsed || usage.Attachments.QuotaBytes != attachmentstore.QuotaBytes {
			t.Fatal("meter did not match the transactional quota charge")
		}
		independent, err := other.StorageUsage()
		if err != nil || independent.Messages.UsedBytes != 0 || independent.Attachments.UsedBytes != 0 {
			t.Fatal("contact usage leaked into another contact")
		}
	}
	assertUsage(0)
	out := testMessage(1, "synthetic queued private text")
	if err := scope.Queue(out); err != nil {
		t.Fatal(err)
	}
	if err := scope.DeleteLocal(out.ID); err != nil {
		t.Fatal(err)
	}
	assertUsage(0)
	if ids, err := scope.PendingIDs(); err != nil || len(ids) != 1 {
		t.Fatal("local deletion lost encrypted outbox charge")
	}
	if err := scope.Acknowledge(out.ID); err != nil {
		t.Fatal(err)
	}
	assertUsage(0)
	usage, _ := scope.StorageUsage()
	if usage.Messages.UsedBytes == 0 {
		t.Fatal("dedup tombstone silently stopped counting toward quota")
	}
	offer := attachmentFixture(20)
	offer.Record.Offer.Size = 128 * 1024
	if err := scope.Attachments().Create(offer); err != nil {
		t.Fatal(err)
	}
	assertUsage(4096)
	accepted, err := scope.Attachments().Change(offer.Record.Offer.ID, func(e *attachmentstore.Entry) error {
		e.Record.State = transfer.Accepted
		e.ObjectID = testID(40)
		e.Key = make([]byte, 32)
		e.Approved = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.CipherBytes != 0 || accepted.Charge() <= offer.Record.Offer.Size {
		t.Fatal("fixture did not reserve a not-yet-received object")
	}
	assertUsage(accepted.Charge())
	draft := attachmentFixture(21)
	draft.Record.State = "draft"
	draft.Record.Direction = transfer.Outgoing
	draft.ObjectID = testID(41)
	draft.Key = make([]byte, 32)
	if err := scope.Attachments().Create(draft); err != nil {
		t.Fatal(err)
	}
	assertUsage(accepted.Charge() + draft.Charge())
	if _, err := scope.Attachments().Change(offer.Record.Offer.ID, func(e *attachmentstore.Entry) error {
		e.Record.State = transfer.Declined
		e.Hidden = true
		e.Approved = false
		e.ObjectID = ""
		e.Key = nil
		e.Units = []attachmentstore.Unit{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertUsage(4096 + draft.Charge())
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if usage, err := scope.StorageUsage(); err == nil || usage.Messages.QuotaBytes != 0 || usage.Attachments.UsedBytes != 0 {
		t.Fatal("closed scope retained usage authority")
	}
}

func TestContactStorageUsageRetainsOutsideWriteAuthentication(t *testing.T) {
	_, scope, _, dir := fixture(t, Options{})
	if err := scope.Queue(testMessage(1, "synthetic private message")); err != nil {
		t.Fatal(err)
	}
	x := external(t, dir)
	if _, err := x.Exec("UPDATE records SET cipher=zeroblob(length(cipher)) WHERE contact_id=? AND sequence=1", scope.id); err != nil {
		t.Fatal(err)
	}
	if usage, err := scope.StorageUsage(); err == nil || usage.Messages.UsedBytes != 0 {
		t.Fatal("meter bypassed outside-write integrity verification")
	}
}
