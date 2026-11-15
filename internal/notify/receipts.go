package notify

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"sync"
	"time"
)

const maxNoticeReceipts = 1024
const noticeReceiptLifetime = 30 * 24 * time.Hour
const noticeReceiptBytes = 16 + 16 + 32 + 8
const noticeReceiptHeader = "WU-NOTICE1"
const maxNoticeReceiptData = len(noticeReceiptHeader) + 2 + maxNoticeReceipts*noticeReceiptBytes

type noticeReceipt struct {
	id, contact string
	proof       [32]byte
	issued      int64
}

// This bounded ledger contains opaque routing identifiers and random proofs,
// never notification text. Platform storage must authenticate and encrypt it.
type noticeReceipts struct {
	mu      sync.Mutex
	entries []noticeReceipt
	save    func([]byte) error
	now     func() time.Time
}

func newNoticeReceipts(data []byte, save func([]byte) error, now func() time.Time) (*noticeReceipts, error) {
	if now == nil {
		now = time.Now
	}
	r := &noticeReceipts{save: save, now: now}
	if len(data) == 0 {
		return r, nil
	}
	if len(data) < len(noticeReceiptHeader)+2 || len(data) > maxNoticeReceiptData || string(data[:len(noticeReceiptHeader)]) != noticeReceiptHeader {
		return nil, ErrUnavailable
	}
	count := int(binary.BigEndian.Uint16(data[len(noticeReceiptHeader):]))
	if count > maxNoticeReceipts || len(data) != len(noticeReceiptHeader)+2+count*noticeReceiptBytes {
		return nil, ErrUnavailable
	}
	data = data[len(noticeReceiptHeader)+2:]
	for range count {
		var entry noticeReceipt
		entry.id = base64.RawURLEncoding.EncodeToString(data[:16])
		entry.contact = base64.RawURLEncoding.EncodeToString(data[16:32])
		copy(entry.proof[:], data[32:64])
		entry.issued = int64(binary.BigEndian.Uint64(data[64:72]))
		if !canonicalID(entry.contact, true) {
			return nil, ErrUnavailable
		}
		r.entries = append(r.entries, entry)
		data = data[noticeReceiptBytes:]
	}
	r.entries = r.current(r.entries)
	return r, nil
}

func (r *noticeReceipts) current(entries []noticeReceipt) []noticeReceipt {
	now := r.now().Unix()
	minimum := r.now().Add(-noticeReceiptLifetime).Unix()
	result := make([]noticeReceipt, 0, len(entries))
	for _, entry := range entries {
		if entry.issued >= minimum && entry.issued <= now {
			result = append(result, entry)
		}
	}
	return result
}

func encodeNoticeReceipts(entries []noticeReceipt) []byte {
	data := make([]byte, len(noticeReceiptHeader)+2, len(noticeReceiptHeader)+2+len(entries)*noticeReceiptBytes)
	copy(data, noticeReceiptHeader)
	binary.BigEndian.PutUint16(data[len(noticeReceiptHeader):], uint16(len(entries)))
	for _, entry := range entries {
		id, _ := base64.RawURLEncoding.Strict().DecodeString(entry.id)
		contact, _ := base64.RawURLEncoding.Strict().DecodeString(entry.contact)
		data = append(data, id...)
		data = append(data, contact...)
		data = append(data, entry.proof[:]...)
		data = binary.BigEndian.AppendUint64(data, uint64(entry.issued))
	}
	return data
}

func (r *noticeReceipts) issue(notice Notice) (string, error) {
	if r == nil || !canonicalID(notice.ID, false) || !canonicalID(notice.ContactID, true) {
		return "", ErrUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entry := noticeReceipt{id: notice.ID, contact: notice.ContactID, issued: r.now().Unix()}
	if _, err := rand.Read(entry.proof[:]); err != nil {
		return "", ErrUnavailable
	}
	entries := append(r.current(r.entries), entry)
	if len(entries) > maxNoticeReceipts {
		entries = entries[len(entries)-maxNoticeReceipts:]
	}
	if r.save == nil || r.save(encodeNoticeReceipts(entries)) != nil {
		return "", ErrUnavailable
	}
	r.entries = entries
	return base64.RawURLEncoding.EncodeToString(entry.proof[:]), nil
}

func (r *noticeReceipts) consume(id, contact, proof string) bool {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(proof)
	if r == nil || err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != proof {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entries := r.current(r.entries)
	for index, entry := range entries {
		if entry.id != id || entry.contact != contact || subtle.ConstantTimeCompare(entry.proof[:], decoded) != 1 {
			continue
		}
		entries = append(entries[:index], entries[index+1:]...)
		if r.save == nil || r.save(encodeNoticeReceipts(entries)) != nil {
			return false
		}
		r.entries = entries
		return true
	}
	return false
}

func (r *noticeReceipts) discard(id, contact, proof string) { _ = r.consume(id, contact, proof) }

func (r *noticeReceipts) forget(contactID string) error {
	if r == nil || !canonicalID(contactID, true) {
		return ErrUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	entries := r.current(r.entries)
	retained := entries[:0]
	for _, entry := range entries {
		if entry.contact != contactID {
			retained = append(retained, entry)
		}
	}
	// Retire the in-process capability even if persistent cleanup is temporarily
	// unavailable. The caller still receives the cleanup error.
	r.entries = retained
	if r.save == nil || r.save(encodeNoticeReceipts(retained)) != nil {
		return ErrUnavailable
	}
	return nil
}
