package messaging

import "time"

const (
	slowReconnectAfter   = 5 * time.Minute
	slowReconnectMinimum = 90 * time.Second
	slowReconnectMaximum = 150 * time.Second
)

// reconnectBackoff belongs to one contact's ordinary connection/setup worker.
// It is protected by Manager.mu and is never persisted or shared with the
// independent, bounded peer-ready handover budget.
type reconnectBackoff struct {
	failedAt time.Time
	attempt  int
}

func (r *reconnectBackoff) nextBounds(now time.Time) (time.Duration, time.Duration) {
	if r.failedAt.IsZero() {
		r.failedAt = now
	}
	if now.Sub(r.failedAt) >= slowReconnectAfter {
		return slowReconnectMinimum, slowReconnectMaximum
	}
	lower, upper := retryBounds(r.attempt)
	if r.attempt < 5 {
		r.attempt++
	}
	return lower, upper
}

func retryBounds(count int) (time.Duration, time.Duration) {
	ceiling := time.Second << min(max(count, 0), 5)
	if ceiling > 30*time.Second {
		ceiling = 30 * time.Second
	}
	floor := ceiling / 2
	if floor < time.Second {
		floor = time.Second
	}
	return floor, ceiling
}

func (m *Manager) reconnectDelay(now time.Time) time.Duration {
	m.mu.Lock()
	lower, upper := m.reconnect.nextBounds(now)
	m.mu.Unlock()
	return jitterRetryDelay(lower, upper)
}
