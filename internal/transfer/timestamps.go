package transfer

import "time"

func ValidRecordedAt(at time.Time) bool {
	if at.IsZero() {
		return true
	}
	_, offset := at.Zone()
	return at.Year() >= 1970 && at.Year() <= 9999 && offset == 0
}

// RecordedAtText exposes only a bounded local event time. A legacy record with
// no known event time remains empty, rather than acquiring a fabricated date.
func RecordedAtText(record Record) string {
	if record.RecordedAt.IsZero() || !ValidRecordedAt(record.RecordedAt) {
		return ""
	}
	return record.RecordedAt.UTC().Format(time.RFC3339Nano)
}
