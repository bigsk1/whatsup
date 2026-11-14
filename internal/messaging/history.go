package messaging

import "whatsup/internal/chat"

// Local history actions never start networking. Clearing history preserves the
// text outbox but permanently withdraws attachments, reclaiming transfer quota.
// Contact and message IDs are checked again under the runtime guard.
func (m *Manager) ClearHistory(id string) (Snapshot, error) {
	if m == nil || m.managerState == nil {
		return m.Snapshot(), ErrUnavailable
	}
	m.mu.Lock()
	if m.refreshLocked() != nil || m.record.ID != id || m.conversation == nil || m.closed || m.state == "failed" {
		snapshot := m.snapshotLocked()
		m.mu.Unlock()
		return snapshot, ErrUnavailable
	}
	engine, conversation := m.transfers, m.conversation
	if engine == nil {
		defer m.mu.Unlock()
		// Keep engine creation excluded until the durable clear has committed.
		// A later session can only load the already-retired attachment set.
		if err := conversation.ClearHistory(); err != nil {
			return m.historyFailureLocked(err)
		}
		m.historyCapacityRecoveredLocked()
		m.bumpLocked()
		return m.snapshotLocked(), nil
	}
	m.mu.Unlock()
	clearHistory := func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed || m.record.ID != id || m.conversation != conversation || m.state == "failed" {
			return ErrUnavailable
		}
		return conversation.ClearHistory()
	}
	err := engine.ClearHistory(clearHistory)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		return m.historyFailureLocked(err)
	}
	m.historyCapacityRecoveredLocked()
	m.bumpLocked()
	return m.snapshotLocked(), nil
}
func (m *Manager) DeleteMessage(id, messageID string) (Snapshot, error) {
	if m == nil || m.managerState == nil {
		return m.Snapshot(), ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocked() != nil || m.record.ID != id || m.conversation == nil || m.closed || m.state == "failed" {
		return m.snapshotLocked(), ErrUnavailable
	}
	if err := m.conversation.DeleteLocal(messageID); err != nil {
		return m.historyFailureLocked(err)
	}
	m.historyCapacityRecoveredLocked()
	m.bumpLocked()
	return m.snapshotLocked(), nil
}
func (m *Manager) historyFailureLocked(err error) (Snapshot, error) {
	if terminalPersistence(err) || err == chat.ErrCorrupt {
		m.state = "failed"
		m.cancel()
		m.bumpLocked()
	}
	return m.snapshotLocked(), err
}
func (m *Manager) historyCapacityRecoveredLocked() {
	if m.state != "capacity-reached" {
		return
	}
	if m.sessionActive {
		m.state = "connected"
	} else if m.running {
		m.state = "connecting"
	} else {
		m.state = "offline"
	}
}
