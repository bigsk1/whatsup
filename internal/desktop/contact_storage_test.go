package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/messagestore"
	"whatsup/internal/transfer"
)

func TestContactStorageBridgeExposesOnlyApprovedLogicalCounters(t *testing.T) {
	s := unlockedInvitationService(t)
	contact := prepareDesktopForgetContact(t, s)
	other := prepareDesktopForgetContact(t, s)
	before, err := s.GetContactStorage(contact.ID)
	if err != nil || before.ContactID != contact.ID || before.MessagesUsedBytes == 0 || before.MessagesQuotaBytes != messagestore.DefaultQuotaBytes || before.AttachmentsUsedBytes != 0 || before.AttachmentsQuotaBytes != attachmentstore.QuotaBytes {
		t.Fatal("approved contact meter missing encrypted outbox or quota")
	}
	scope, err := s.application.AttachmentStore(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic attachment contents")
	offer := transfer.Offer{ID: "AwMDAwMDAwMDAwMDAwMDAw", Name: "synthetic.pdf", MediaType: "application/pdf", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
	if err := scope.Stage(context.Background(), offer, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	usage, err := s.GetContactStorage(contact.ID)
	if err != nil || usage.AttachmentsUsedBytes <= uint64(len(body)) || usage.MessagesUsedBytes != before.MessagesUsedBytes {
		t.Fatal("draft reservation missing or text accounting modified")
	}
	otherUsage, err := s.GetContactStorage(other.ID)
	if err != nil || otherUsage.AttachmentsUsedBytes != 0 {
		t.Fatal("meter crossed saved contact boundary")
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || len(fields) != 5 {
		t.Fatal("storage meter exposed more than the five reviewed fields")
	}
	for _, name := range []string{"contactID", "messagesUsedBytes", "messagesQuotaBytes", "attachmentsUsedBytes", "attachmentsQuotaBytes"} {
		if _, ok := fields[name]; !ok {
			t.Fatal("storage meter field contract changed")
		}
	}
	if _, err := s.ForgetContact(contact.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{contact.ID, "", "not a contact"} {
		if got, err := s.GetContactStorage(id); err == nil || got != (ContactStorage{}) {
			t.Fatal("unknown/forgotten contact exposed usage")
		}
	}
	s.LockStorage()
	if got, err := s.GetContactStorage(other.ID); err == nil || got != (ContactStorage{}) {
		t.Fatal("locked storage exposed a retained contact meter")
	}
}
