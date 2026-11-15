package messagestore

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"whatsup/internal/attachmentstore"
	"whatsup/internal/transfer"
)

func TestLegacyAttachmentTimestampRemainsAbsentAndCanonicalOnReopen(t *testing.T) {
	db, scope, key, dir := fixture(t, Options{})
	legacy := attachmentFixture(1)
	if err := scope.Attachments().Create(legacy); err != nil {
		t.Fatal(err)
	}
	var blob []byte
	var sequence uint64
	if err := db.conn.QueryRowContext(t.Context(), "SELECT sequence,cipher FROM attachments WHERE contact_id=? AND message_id=?", scope.id, legacy.Record.Offer.ID).Scan(&sequence, &blob); err != nil {
		t.Fatal(err)
	}
	aead, err := db.aead("attachment", scope.id)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := aead.Open(nil, blob[:24], blob[24:], aad("attachment", scope.id, legacy.Record.Offer.ID, sequence))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	if bytes.Contains(plain, []byte("RecordedAt")) {
		t.Fatal("zero timestamp changed canonical legacy codec")
	}
	db, scope = reopen(t, db, key, dir, Options{})
	got, err := scope.Attachments().Get(legacy.Record.Offer.ID)
	if err != nil || !got.Record.RecordedAt.IsZero() {
		t.Fatal("legacy timestamp missing field was not readable")
	}
	if _, err := scope.Attachments().Change(legacy.Record.Offer.ID, func(e *attachmentstore.Entry) error { e.Record.State = transfer.Declined; return nil }); err != nil {
		t.Fatal(err)
	}
	got, err = scope.Attachments().Get(legacy.Record.Offer.ID)
	if err != nil || !got.Record.RecordedAt.IsZero() {
		t.Fatal("legacy lifecycle invented historical time")
	}
}

func TestAttachmentTimestampCannotBeChangedByProgressOrErased(t *testing.T) {
	_, scope, _, _ := fixture(t, Options{})
	known := attachmentFixture(1)
	known.Record.RecordedAt = time.Date(2026, 11, 14, 1, 2, 3, 123, time.UTC)
	if err := scope.Attachments().Create(known); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{time.Time{}, known.Record.RecordedAt.Add(time.Hour)} {
		if _, err := scope.Attachments().Change(known.Record.Offer.ID, func(e *attachmentstore.Entry) error { e.Record.RecordedAt = at; return nil }); !errors.Is(err, transfer.ErrIntegrity) {
			t.Fatal("known local timestamp was mutable")
		}
	}
	legacy := attachmentFixture(2)
	if err := scope.Attachments().Create(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := scope.Attachments().Change(legacy.Record.Offer.ID, func(e *attachmentstore.Entry) error { e.Record.RecordedAt = known.Record.RecordedAt; return nil }); !errors.Is(err, transfer.ErrIntegrity) {
		t.Fatal("legacy incoming history acquired invented event time")
	}
}
