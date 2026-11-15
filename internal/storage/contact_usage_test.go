package storage

import (
	"bytes"
	"context"
	"testing"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/messagestore"
)

func TestContactStorageUsageRequiresExactLocalApprovalAndRetiresWithContact(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	pending := ongoingRecord(t, app, 201)
	if err := app.SavePendingContact(pending); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ContactStorageUsage(pending.ID); err == nil {
		t.Fatal("pending peer obtained local storage usage")
	}
	if _, err := app.CommitPendingContact(pending); err != nil {
		t.Fatal(err)
	}
	if _, err := app.ContactStorageUsage(pending.ID); err == nil {
		t.Fatal("committed setup without local approval obtained usage")
	}
	record, _, _, _, _, _ := prepareForgetStorage(t, app, 202)
	other, _, _, _, _, _ := prepareForgetStorage(t, app, 203)
	scope, err := app.AttachmentStore(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("synthetic object"), 1024)
	offer := storageAttachmentOffer(11, body)
	if err := scope.Stage(context.Background(), offer, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	usage, err := app.ContactStorageUsage(record.ID)
	if err != nil || usage.Messages.QuotaBytes != messagestore.DefaultQuotaBytes || usage.Attachments.QuotaBytes != attachmentstore.QuotaBytes || usage.Attachments.UsedBytes <= uint64(len(body)) {
		t.Fatal("approved contact did not report its full staging reservation")
	}
	independent, err := app.ContactStorageUsage(other.ID)
	if err != nil || independent.Attachments.UsedBytes != 0 {
		t.Fatal("usage was shared across contact boundaries")
	}
	if err := app.ForgetCommittedContact(record, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if usage, err := app.ContactStorageUsage(record.ID); err == nil || usage.Messages.UsedBytes != 0 || usage.Attachments.QuotaBytes != 0 {
		t.Fatal("forgotten contact retained usage authority")
	}
	if app.Close() != nil {
		t.Fatal("close failed")
	}
	if _, err := app.ContactStorageUsage(other.ID); err == nil {
		t.Fatal("locked application exposed another contact's usage")
	}
}

func TestContactStorageUsageOfNewApprovedContactDoesNotCreateDatabaseScope(t *testing.T) {
	_, app := openApplicationTest(t, privateDirectory(t), &memoryKeys{})
	record, pin := commitOngoing(t, app, 204)
	if err := app.ApproveCommittedConversation(record, pin); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		scope, err := app.messages.ExistingScope(record.ID)
		if err != nil || scope != nil {
			t.Fatal("usage read manufactured an empty database contact")
		}
		usage, err := app.ContactStorageUsage(record.ID)
		if err != nil || usage.Messages.UsedBytes != 0 || usage.Attachments.UsedBytes != 0 || usage.Messages.QuotaBytes != messagestore.DefaultQuotaBytes || usage.Attachments.QuotaBytes != attachmentstore.QuotaBytes {
			t.Fatal("new approved contact did not report empty logical budgets")
		}
	}
}
