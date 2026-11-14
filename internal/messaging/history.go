package messaging

import "whatsup/internal/chat"

// Local history actions never start networking or discard pending delivery.
// Contact and message IDs are checked again under the runtime guard.
func (m *Manager) ClearHistory(id string) (Snapshot, error) {
	if m == nil || m.managerState == nil {
		return m.Snapshot(), ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocked() != nil || m.record.ID != id || m.conversation == nil || m.closed || m.state == "failed" {
		return m.snapshotLocked(), ErrUnavailable
	}
	if err := m.conversation.ClearHistory(); err != nil {
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
