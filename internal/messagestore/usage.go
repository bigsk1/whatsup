package messagestore

import (
	"context"
	"database/sql"

	"whatsup/internal/attachmentstore"
)

type ContactUsage struct {
	Messages    Usage
	Attachments attachmentstore.Usage
}

// StorageUsage reads both authenticated counters in one transaction. Bodies and
// attachment entries are not scanned/decrypted just to display a usage meter.
// Normal outside-write verification and scoped epoch checks still apply.
func (s *Scope) StorageUsage() (usage ContactUsage, err error) {
	if s == nil || s.db == nil {
		return ContactUsage{}, ErrClosed
	}
	err = s.db.transaction(func(ctx context.Context, tx *sql.Tx) error {
		m, _, err := s.meta(ctx, tx)
		if err != nil {
			return err
		}
		attachments, err := attachmentstore.ChargedUsage(m.AttachmentUsed)
		if err != nil {
			return ErrCorrupt
		}
		quota := s.db.options.QuotaBytes
		usage = ContactUsage{Messages: Usage{UsedBytes: m.UsedBytes, QuotaBytes: quota, Full: m.UsedBytes >= quota || quota-m.UsedBytes < minimumRecordCharge}, Attachments: attachments}
		return nil
	})
	if err != nil {
		return ContactUsage{}, err
	}
	return usage, nil
}
