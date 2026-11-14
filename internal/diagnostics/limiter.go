package diagnostics

import (
	"sync"
	"time"
)

func failureEvent(event Event) bool { return event >= RelayFetchFailed && event <= TransferFailed }

// RateLimited shares one finite failure-code budget between contact runtimes.
// It retains no peer IDs or errors. Ordinary lifecycle codes are unchanged.
func RateLimited(record func(Event)) func(Event) {
	return rateLimited(record, time.Now)
}

func rateLimited(record func(Event), now func() time.Time) func(Event) {
	var mu sync.Mutex
	var last [TransferFailed + 1]time.Time
	return func(event Event) {
		if record == nil {
			return
		}
		if _, err := eventName(event); err != nil {
			return
		}
		if failureEvent(event) {
			at := now()
			mu.Lock()
			if !last[event].IsZero() && at.Sub(last[event]) < FailureInterval {
				mu.Unlock()
				return
			}
			last[event] = at
			mu.Unlock()
		}
		record(event)
	}
}
