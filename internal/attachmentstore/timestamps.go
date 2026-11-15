package attachmentstore

import "whatsup/internal/transfer"

// ValidRecordedAtChange keeps the first local offer/queue event immutable. An
// unsent draft can acquire its time exactly when the user first queues it.
// Progress or retry of a legacy zero-time record never invents historical time.
func ValidRecordedAtChange(previous, next transfer.Record) bool {
	if !transfer.ValidRecordedAt(next.RecordedAt) {
		return false
	}
	if previous.RecordedAt.Equal(next.RecordedAt) {
		return true
	}
	return previous.RecordedAt.IsZero() && !next.RecordedAt.IsZero() && previous.Direction == transfer.Outgoing && next.Direction == transfer.Outgoing && previous.State == transfer.Draft && next.State == transfer.Queued
}
