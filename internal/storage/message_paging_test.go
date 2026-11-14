package storage

import (
	"errors"
	"testing"

	"whatsup/internal/message"
)

func TestScopedMessagePagingAndUnreadCannotOutliveApproval(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, pin := commitOngoing(t, app, 101)
	if err := app.ApproveCommittedConversation(record, pin); err != nil {
		t.Fatal(err)
	}
	store, err := app.ChatStore(pin)
	if err != nil {
		t.Fatal(err)
	}
	var _ message.PagedRepository = store
	item := queuedMessageTest("AAAAAAAAAAAAAAAAAAAAAQ", "synthetic scoped unread")
	if _, err := store.Receive(item); err != nil {
		t.Fatal(err)
	}
	if count, err := store.UnreadCount(); err != nil || count != 1 {
		t.Fatal("scoped unread did not reflect committed receive")
	}
	page, err := store.ReadPage(0, 10)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != item.ID {
		t.Fatal("scoped page did not reflect committed receive")
	}
	if err := store.MarkRead(item.ID); err != nil {
		t.Fatal(err)
	}
	if count, err := store.UnreadCount(); err != nil || count != 0 {
		t.Fatal("scoped read marker did not persist")
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadPage(0, 10); !errors.Is(err, ErrLocked) {
		t.Fatal("closed application still read a page")
	}
	if _, err := store.UnreadCount(); !errors.Is(err, ErrLocked) {
		t.Fatal("closed application still read unread metadata")
	}
	if err := store.MarkRead(item.ID); !errors.Is(err, ErrLocked) {
		t.Fatal("closed application still changed read state")
	}
}
