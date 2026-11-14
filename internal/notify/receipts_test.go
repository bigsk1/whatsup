package notify

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestIssuedReceiptRequiresExactContextAndSurvivesReopen(t *testing.T) {
	now := time.Unix(1800000000, 0)
	var saved []byte
	save := func(data []byte) error { saved = bytes.Clone(data); return nil }
	r, err := newNoticeReceipts(nil, save, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	notice := syntheticNotice()
	proof, err := r.issue(notice)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(saved, []byte(notice.Title)) || bytes.Contains(saved, []byte(notice.Body)) {
		t.Fatal("receipt retained content")
	}
	reopened, err := newNoticeReceipts(saved, save, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for _, values := range [][3]string{{notice.ContactID, notice.ContactID, proof}, {notice.ID, notice.ID, proof}, {notice.ID, notice.ContactID, "invalid"}} {
		if reopened.consume(values[0], values[1], values[2]) {
			t.Fatal("unissued context accepted")
		}
	}
	if !reopened.consume(notice.ID, notice.ContactID, proof) || reopened.consume(notice.ID, notice.ContactID, proof) {
		t.Fatal("receipt not consumed exactly once")
	}
	last, err := newNoticeReceipts(saved, save, func() time.Time { return now })
	if err != nil || last.consume(notice.ID, notice.ContactID, proof) {
		t.Fatal("consumed proof revived after restart")
	}
}

func TestReceiptBoundsExpiryAndPersistenceFailures(t *testing.T) {
	now := time.Unix(1800000000, 0)
	saveFailure := false
	r, _ := newNoticeReceipts(nil, func([]byte) error {
		if saveFailure {
			return errors.New("unavailable")
		}
		return nil
	}, func() time.Time { return now })
	first, _ := r.issue(syntheticNotice())
	for range maxNoticeReceipts {
		if _, err := r.issue(syntheticNotice()); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.entries) != maxNoticeReceipts || r.consume(syntheticNotice().ID, syntheticNotice().ContactID, first) {
		t.Fatal("old receipts not evicted at bound")
	}
	proof, _ := r.issue(syntheticNotice())
	saveFailure = true
	if _, err := r.issue(syntheticNotice()); err == nil || r.consume(syntheticNotice().ID, syntheticNotice().ContactID, proof) {
		t.Fatal("failed durable ledger write permitted activation")
	}
	saveFailure = false
	if !r.consume(syntheticNotice().ID, syntheticNotice().ContactID, proof) {
		t.Fatal("failed consumption lost legitimate notice")
	}
	proof, _ = r.issue(syntheticNotice())
	now = now.Add(noticeReceiptLifetime + time.Second)
	if r.consume(syntheticNotice().ID, syntheticNotice().ContactID, proof) {
		t.Fatal("expired proof accepted")
	}
	for _, data := range [][]byte{{1, 2, 3}, append(encodeNoticeReceipts(nil), 0), make([]byte, maxNoticeReceiptData+1)} {
		if _, err := newNoticeReceipts(data, func([]byte) error { return nil }, nil); err == nil {
			t.Fatal("malformed ledger accepted")
		}
	}
}
