package messagestore

import (
	"context"
	"testing"
	"time"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/transfer"
)

func TestPendingWithdrawalBatchesRefillWithoutActiveSlotsAndSurviveReopen(t *testing.T) {
	db, scope, key, dir := fixture(t, Options{})
	const total = 2*transfer.MaxOffers + 3
	for i := 1; i <= total; i++ {
		entry := attachmentFixture(uint64(i))
		entry.Record.State = transfer.Declined
		entry.Record.PendingWithdrawal = true
		entry.Hidden = true
		if err := scope.Attachments().Create(entry); err != nil {
			t.Fatal(err)
		}
	}
	db, scope = reopen(t, db, key, dir, Options{})
	active, err := scope.Attachments().Active()
	if err != nil || len(active) != 0 {
		t.Fatal("pending receipts retained active file slots", err)
	}
	entries, err := scope.Attachments().PendingWithdrawals()
	if err != nil || len(entries) != transfer.MaxOffers {
		t.Fatal("pending control batch was not bounded", err)
	}
	// An attachment scope supplies only bounded batches. Exercise the engine
	// with this real SQLite metadata through the real object store capability.
	objects, err := attachmentstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer objects.Close()
	objectsScope, err := objects.Scope(scope.id, scope.Attachments())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := transfer.New(objectsScope, transfer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	epoch, err := engine.Begin(context.Background(), func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range total {
		select {
		case out := <-engine.Output():
			f := out.Frame
			if f.Kind != transfer.WithdrawFrame || seen[f.ID] {
				t.Fatal("batch repeated a live cancellation or sent unrelated work")
			}
			seen[f.ID] = true
			if err := engine.Submit(epoch, transfer.Frame{Kind: transfer.WithdrawAckFrame, ID: f.ID, SHA256: f.SHA256, Reason: transfer.ReasonDeclined}); err != nil {
				t.Fatal(err)
			}
		case failure := <-engine.Failures():
			t.Fatal("pending history overflowed session controls", failure.Err)
		case <-time.After(3 * time.Second):
			t.Fatal("pending cancellation batch did not refill")
		}
	}
	if _, err := engine.Summaries(); err != nil {
		t.Fatal(err)
	}
	pending, err := scope.Attachments().PendingWithdrawals()
	if err != nil || len(pending) != 0 {
		t.Fatal("acknowledged intents were not retired", err)
	}
	engine.Close()
	if err := objects.Close(); err != nil {
		t.Fatal(err)
	}
	db, scope = reopen(t, db, key, dir, Options{})
	pending, err = scope.Attachments().PendingWithdrawals()
	if err != nil || len(pending) != 0 {
		t.Fatal("reopen resurrected acknowledged cancellation", err)
	}
}
