package desktop

import "testing"

func TestLocalHistoryActionsKeepContactAndPendingDelivery(t *testing.T) {
	for _, clearAll := range []bool{false, true} {
		t.Run(map[bool]string{false: "one message", true: "all history"}[clearAll], func(t *testing.T) {
			s := unlockedInvitationService(t)
			record := prepareDesktopForgetContact(t, s)
			before := s.GetStatus().IdentityFingerprint
			initial := s.GetConversation()
			if len(initial.Messages) != 1 {
				t.Fatal("fixture has no queued message")
			}
			var err error
			if clearAll {
				_, err = s.ClearHistory(record.ID)
			} else {
				_, err = s.DeleteMessage(record.ID, initial.Messages[0].ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			got := s.GetConversation()
			if got.ContactID != record.ID || len(got.Messages) != 0 || !got.CanSend || s.GetStatus().IdentityFingerprint != before {
				t.Fatal("local deletion removed contact or identity")
			}
			store, err := s.application.ChatStore(forgetTestPin{record})
			if err != nil {
				t.Fatal(err)
			}
			pending, err := store.PendingIDs()
			if err != nil || len(pending) != 1 || pending[0] != initial.Messages[0].ID {
				t.Fatal("local deletion silently discarded pending delivery")
			}
			if s.LockStorage().StorageState != "locked" {
				t.Fatal("lock failed")
			}
			if _, err = s.UnlockStorage(); err != nil {
				t.Fatal(err)
			}
			if got = s.GetConversation(); got.ContactID != record.ID || len(got.Messages) != 0 || !got.CanSend {
				t.Fatal("local deletion or approval did not survive reopen")
			}
		})
	}
}

func TestHistoryActionsRejectStaleContactAndLockedState(t *testing.T) {
	s := unlockedInvitationService(t)
	record := prepareDesktopForgetContact(t, s)
	messageID := s.GetConversation().Messages[0].ID
	if _, err := s.ClearHistory("AgICAgICAgICAgICAgICAg"); err == nil {
		t.Fatal("another contact target cleared history")
	}
	if _, err := s.DeleteMessage("AgICAgICAgICAgICAgICAg", messageID); err == nil {
		t.Fatal("another contact target deleted a message")
	}
	if len(s.GetConversation().Messages) != 1 {
		t.Fatal("stale action modified history")
	}
	s.LockStorage()
	if _, err := s.ClearHistory(record.ID); err == nil {
		t.Fatal("locked clear succeeded")
	}
	if _, err := s.DeleteMessage(record.ID, messageID); err == nil {
		t.Fatal("locked deletion succeeded")
	}
}
