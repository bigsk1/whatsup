package messaging

import "whatsup/internal/transport"

// SetPaused is local policy only. Persist it for the exact approved association,
// then join its old runtime before opening another local history handle or
// allowing the peer to authenticate again. Other contact runtimes stay live.
func (r *Registry) SetPaused(id string, paused bool) (ContactSummary, error) {
	unavailable := ContactSummary{State: "unavailable", Path: string(transport.PathUnknown)}
	if r == nil || r.registryState == nil {
		return unavailable, ErrUnavailable
	}
	r.mu.Lock()
	m, err := r.ensureLocked(id)
	if err != nil {
		r.mu.Unlock()
		return unavailable, err
	}
	m.mu.Lock()
	if m.refreshLocked() != nil || m.record.ID != id || m.conversation == nil || m.removing {
		m.mu.Unlock()
		r.mu.Unlock()
		return unavailable, ErrUnavailable
	}
	if m.paused == paused {
		summary := m.summaryLocked()
		m.mu.Unlock()
		r.mu.Unlock()
		return summary, nil
	}
	if err := r.application.SetContactPaused(id, paused); err != nil {
		if terminalPersistence(err) {
			m.failLocked()
		}
		summary := m.summaryLocked()
		m.mu.Unlock()
		r.mu.Unlock()
		return summary, err
	}

	// Keep this generation gated even when resuming. A goroutine that already
	// acquired it must never be able to start work on this retired lifetime.
	m.paused = true
	m.cancel()
	r.transition[id] = true
	delete(r.managers, id)
	r.retirees.Add(1)
	m.mu.Unlock()
	r.mu.Unlock()
	defer r.retirees.Done()
	// Do not hold registry, manager or vault locks while joining I/O and
	// callbacks. A concurrent Close waits for this join before closing the vault.
	m.Close()
	m.mu.Lock()
	previous := m.state
	m.mu.Unlock()

	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.transition, id)
	replacement, err := r.ensureLocked(id)
	if err != nil {
		return unavailable, err
	}
	replacement.mu.Lock()
	defer replacement.mu.Unlock()
	// Toggling must not clear any terminal failure for which the ordinary
	// worker stopped. Resume an otherwise healthy contact with its saved pin.
	switch previous {
	case "failed", "identity-changed":
		replacement.state = previous
		replacement.cancel()
	case "update-required", "capacity-reached", "peer-capacity-reached":
		if replacement.state == "offline" {
			replacement.state = previous
		}
	}
	if !paused && replacement.state == "offline" && replacement.canConnectLocked() {
		replacement.connectLocked()
	}
	return replacement.summaryLocked(), nil
}
