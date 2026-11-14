package desktop

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"whatsup/internal/messaging"
)

func TestContactsPagesAndForgetAreIndependent(t *testing.T) {
	s := unlockedInvitationService(t)
	first := prepareDesktopForgetContact(t, s)
	second := prepareDesktopForgetContact(t, s)
	if first.PresharedKey == second.PresharedKey {
		t.Fatal("contacts shared a PSK")
	}
	contacts, err := s.GetContacts()
	if err != nil || len(contacts) != 2 {
		t.Fatal("separate approved contacts were not listed")
	}
	encoded, err := json.Marshal(contacts)
	if err != nil || strings.Contains(string(encoded), "synthetic queued message") || strings.Contains(string(encoded), "messages") || strings.Contains(string(encoded), first.PresharedKey) {
		t.Fatal("contact summary exposed message bodies or credentials")
	}
	if _, err := s.SendMessage(second.ID, "synthetic second contact only"); err != nil {
		t.Fatal(err)
	}
	left, err := s.GetMessages(first.ID, "", 1)
	if err != nil || len(left.Messages) != 1 || left.HasMore || left.ContactID != first.ID {
		t.Fatal("first contact page was not isolated")
	}
	right, err := s.GetMessages(second.ID, "", 1)
	if err != nil || len(right.Messages) != 1 || right.Messages[0].Text != "synthetic second contact only" || !right.HasMore || right.NextCursor == "" {
		t.Fatal("second contact page was not bounded")
	}
	older, err := s.GetMessages(second.ID, right.NextCursor, 1)
	if err != nil || len(older.Messages) != 1 || older.Messages[0].ID == right.Messages[0].ID || older.HasMore {
		t.Fatal("paging repeated or lost a message")
	}
	if _, err := s.SetContactName(first.ID, "Synthetic first"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetContactName(second.ID, "Synthetic second"); err != nil {
		t.Fatal(err)
	}
	registry, pairing, invitation := s.messages, s.pairing, s.invitations
	if _, err := s.ForgetContact(first.ID); err != nil {
		t.Fatal(err)
	}
	if s.messages != registry || s.pairing != pairing || s.invitations != invitation {
		t.Fatal("forget replaced other contact controllers")
	}
	if _, err := s.messages.Connect(first.ID); err == nil {
		t.Fatal("late completion restarted forgotten contact")
	}
	remaining, err := s.GetContacts()
	if err != nil || len(remaining) != 1 || remaining[0].ContactID != second.ID || remaining[0].LocalName != "Synthetic second" {
		t.Fatal("forget changed unrelated contact")
	}
	if _, err := s.SendMessage(second.ID, "synthetic surviving contact remains usable"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClearHistory(second.ID); err != nil {
		t.Fatal(err)
	}
	page, err := s.GetMessages(second.ID, "", 100)
	if err != nil || len(page.Messages) != 0 || page.HasMore {
		t.Fatal("clear history did not preserve an empty contact")
	}
	if s.LockStorage().StorageState != "locked" {
		t.Fatal("lock failed")
	}
	if contacts, err := s.GetContacts(); err != nil || len(contacts) != 0 {
		t.Fatal("locked contact list exposed names")
	}
	if _, err := s.GetMessages(second.ID, "", 100); err == nil {
		t.Fatal("locked page exposed messages")
	}
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	remaining, err = s.GetContacts()
	if err != nil || len(remaining) != 1 || remaining[0].ContactID != second.ID || !remaining[0].CanSend {
		t.Fatal("unrelated contact did not survive reopen")
	}
}

func TestPagedBridgeRejectsInvalidTargetsAndCursors(t *testing.T) {
	s := unlockedInvitationService(t)
	r := prepareDesktopForgetContact(t, s)
	for _, cursor := range []string{"-1", "0", "01", "18446744073709551616", "../state.vault"} {
		if _, err := s.GetMessages(r.ID, cursor, 10); err == nil {
			t.Fatal("invalid page cursor accepted")
		}
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, err := s.GetMessages(r.ID, "", limit); err == nil {
			t.Fatal("unbounded page limit accepted")
		}
	}
	if _, err := s.GetMessages("stale", "", 10); err == nil {
		t.Fatal("unknown contact page accepted")
	}
	if _, err := s.MarkRead("stale", "stale"); err == nil {
		t.Fatal("unknown read marker accepted")
	}
	if _, err := s.MarkRead(r.ID, "stale"); err == nil {
		t.Fatal("unknown message read marker accepted")
	}
}

func TestContactEventsAreCoalescedAndDeliveredOutsideServiceLock(t *testing.T) {
	var s *Service
	observed := make(chan messaging.Hint, 16)
	s = NewWithEvents(filepath.Join(t.TempDir(), "private"), &testKeys{}, nil, func(h messaging.Hint) {
		_ = s.GetStatus() // A synchronous Wails hook can safely read the service.
		observed <- h
	})
	t.Cleanup(func() { _ = s.ServiceShutdown() })
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	r := prepareDesktopForgetContact(t, s)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case h := <-observed:
			if h.ContactID == r.ID && h.Revision > 0 {
				goto received
			}
		case <-deadline.C:
			t.Fatal("event callback deadlocked behind service lock")
		}
	}
received:
	before, err := s.GetContacts()
	if err != nil || len(before) != 1 {
		t.Fatal("contact unavailable")
	}
	s.LockStorage()
	if _, err := s.UnlockStorage(); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetContacts()
	if err != nil || len(after) != 1 || after[0].Revision <= before[0].Revision {
		t.Fatal("unlock reset the contact revision clock")
	}
}
