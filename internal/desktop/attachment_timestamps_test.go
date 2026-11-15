package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"whatsup/internal/transfer"
)

func TestAttachmentViewPublishesLocalTimestampAndRetainsItAfterUnlock(t *testing.T) {
	s, contact := attachmentService(t, nil)
	scope, err := s.application.AttachmentStore(contact.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("synthetic local attachment event")
	offer := transfer.Offer{ID: "BgYGBgYGBgYGBgYGBgYGBg", Name: "synthetic.pdf", MediaType: "application/pdf", Size: uint64(len(body)), SHA256: sha256.Sum256(body)}
	if err := scope.Stage(context.Background(), offer, bytes.NewReader(body)); err != nil {
		t.Fatal(err)
	}
	if err := scope.QueueOutgoing(offer.ID, false); err != nil {
		t.Fatal(err)
	}
	record, err := scope.Get(offer.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := transfer.RecordedAtText(record)
	for run := range 2 {
		if run == 1 {
			s.LockStorage()
			if _, err := s.UnlockStorage(); err != nil {
				t.Fatal(err)
			}
		}
		list, err := s.GetAttachments(contact.ID)
		if err != nil || len(list.Items) != 1 || list.Items[0].RecordedAt != want || want == "" {
			t.Fatal("attachment summary omitted stable local event time")
		}
		if at, err := time.Parse(time.RFC3339Nano, list.Items[0].RecordedAt); err != nil || at.IsZero() {
			t.Fatal("attachment bridge timestamp was not bounded RFC3339Nano")
		}
	}
}
