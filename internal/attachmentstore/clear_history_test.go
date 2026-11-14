package attachmentstore_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/transfer"
)

func TestClearHistoryWithdrawsQueuedAndAcceptedTransfersAndReclaimsObjects(t *testing.T) {
	f := newFixture(t)
	body := bytes.Repeat([]byte("synthetic attachment clear boundary"), 40000)
	out, in := offer(81, body), offer(82, body)
	otherMetadata, err := f.db.Scope(id(91))
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.store.Scope(id(91), otherMetadata.Attachments())
	if err != nil {
		t.Fatal(err)
	}
	otherOffer := offer(92, body)
	if err := other.Stage(context.Background(), otherOffer, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := other.QueueOutgoing(otherOffer.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := f.scope.Stage(context.Background(), out, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := f.scope.QueueOutgoing(out.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := f.scope.Offer(in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.scope.Accept(in.ID); err != nil {
		t.Fatal(err)
	}
	appendBody(t, f.scope, in, body, 0)
	before, err := f.raw.Attachments().Active()
	if err != nil || len(before) != 2 {
		t.Fatal("missing active transfer fixture", err)
	}
	if err := f.raw.ClearHistory(); err != nil {
		t.Fatal(err)
	}
	if err := f.scope.CollectGarbage(); err != nil {
		t.Fatal(err)
	}
	for _, old := range before {
		entry, err := f.raw.Attachments().Get(old.Record.Offer.ID)
		if err != nil || !entry.Hidden || entry.Record.State != transfer.Declined || entry.Active() || entry.ObjectID != "" || entry.Charge() != 4096 {
			t.Fatal("clear retained hidden work, active slot or object quota", err)
		}
		if _, err := os.Lstat(filepath.Join(f.dir, attachmentstore.Filename, old.ObjectID+".bin")); !os.IsNotExist(err) {
			t.Fatal("clear retained the encrypted object", err)
		}
		if _, err := f.scope.Resume(old.Record.Offer.ID); !errors.Is(err, transfer.ErrUnavailable) {
			t.Fatal("clear allowed a cancelled transfer to resume", err)
		}
	}
	active, err := f.raw.Attachments().Active()
	if err != nil || len(active) != 0 {
		t.Fatal("clear retained an active transfer slot", err)
	}
	otherRecord, err := other.Get(otherOffer.ID)
	if err != nil || otherRecord.Hidden || otherRecord.State != transfer.Queued {
		t.Fatal("clear changed another contact's queued file", err)
	}
	reader, err := other.OpenOutgoing(context.Background(), otherOffer.ID)
	if err != nil {
		t.Fatal("clear revoked another contact's source", err)
	}
	_ = reader.Close()
	f.close(t)
	f.open(t)
	if err := f.scope.Offer(in); err != nil {
		t.Fatal(err)
	}
	entry, err := f.scope.Get(in.ID)
	if err != nil || !entry.Hidden || entry.State != transfer.Declined {
		t.Fatal("replayed offer revived cleared transfer", err)
	}
}

func TestReopenRetiresLegacyHiddenQueuedTransfer(t *testing.T) {
	f := newFixture(t)
	body := []byte("synthetic legacy hidden pending attachment")
	o := offer(83, body)
	if err := f.scope.Stage(context.Background(), o, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := f.scope.QueueOutgoing(o.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.raw.Attachments().Change(o.ID, func(e *attachmentstore.Entry) error {
		e.Hidden = true // Persisted by Clear history in the previous implementation.
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.close(t)
	f.open(t)
	e, err := f.raw.Attachments().Get(o.ID)
	if err != nil || !e.Hidden || e.Active() || e.ObjectID != "" || e.Record.State != transfer.Declined {
		t.Fatal("reopen resumed legacy hidden attachment", err)
	}
}
