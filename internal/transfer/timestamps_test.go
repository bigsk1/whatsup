package transfer

import (
	"testing"
	"time"
)

func TestRecordedAtPublicValueIsBoundedUTCOrUnknown(t *testing.T) {
	for _, at := range []time.Time{time.Time{}, time.Date(1969, 12, 31, 0, 0, 0, 0, time.UTC), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 14, 0, 0, 0, 0, time.FixedZone("other", 3600))} {
		if value := RecordedAtText(Record{RecordedAt: at}); value != "" {
			t.Fatal("unknown/out-of-range/nonlocal timestamp exposed")
		}
	}
	at := time.Date(2026, 11, 14, 1, 2, 3, 123456789, time.UTC)
	value := RecordedAtText(Record{RecordedAt: at})
	if value != "2026-11-14T01:02:03.123456789Z" || len(value) > 30 {
		t.Fatal("local timestamp lost precision or exceeded bridge bound")
	}
}
