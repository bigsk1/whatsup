package messaging

import (
	"time"

	"whatsup/internal/diagnostics"
	"whatsup/internal/transport"
)

const (
	PathSampleInterval = 5 * time.Second
	PathMaxAge         = 2 * PathSampleInterval
)

// Incoming stays inside Go. A notification adapter must recheck live local
// approval and notification preferences before accessing any optional preview.
type Incoming struct {
	ContactID string
	MessageID string
	origin    *managerState
}

func (Incoming) String() string               { return "[private WhatsUp committed-message notice]" }
func (Incoming) GoString() string             { return "[private WhatsUp committed-message notice]" }
func (Incoming) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func (m *Manager) report(event diagnostics.Event) {
	if event != 0 && m.diagnostics != nil {
		m.diagnostics(event)
	}
}

func (m *Manager) notifyIncoming(messageID string) {
	m.mu.Lock()
	callback, id, live := m.onIncoming, m.record.ID, !m.closed && !m.paused && m.sessionActive
	m.mu.Unlock()
	if live && callback != nil && id != "" {
		callback(Incoming{ContactID: id, MessageID: messageID, origin: m.managerState})
	}
}

func (m *Manager) pathLocked(now time.Time) transport.Path {
	if m.closed || m.paused || !m.sessionActive || m.pathAt.IsZero() || now.Sub(m.pathAt) < 0 || now.Sub(m.pathAt) >= PathMaxAge {
		return transport.PathUnknown
	}
	if m.path == transport.PathDirect || m.path == transport.PathRelayed {
		return m.path
	}
	return transport.PathUnknown
}

func (m *Manager) clearPathLocked() {
	previous := m.path
	m.path, m.pathAt = transport.PathUnknown, time.Time{}
	m.pathEpoch++
	if m.pathTimer != nil {
		m.pathTimer.Stop()
		m.pathTimer = nil
	}
	if previous != "" && previous != transport.PathUnknown {
		m.bumpLocked()
	}
}

func (m *Manager) samplePath() {
	m.mu.Lock()
	raw, live := m.raw, !m.closed && m.sessionActive
	m.mu.Unlock()
	if !live {
		return
	}
	path := transport.PathUnknown
	if source, ok := raw.(interface{ Path() transport.Path }); ok {
		path = source.Path()
	}
	if path != transport.PathDirect && path != transport.PathRelayed {
		path = transport.PathUnknown
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || !m.sessionActive || m.raw != raw {
		return
	}
	previous := m.pathLocked(time.Now())
	m.path, m.pathAt = path, time.Now()
	m.pathEpoch++
	epoch := m.pathEpoch
	if m.pathTimer != nil {
		m.pathTimer.Stop()
	}
	if path != transport.PathUnknown {
		m.pathTimer = time.AfterFunc(PathMaxAge, func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if !m.closed && m.sessionActive && m.pathEpoch == epoch {
				m.path, m.pathAt = transport.PathUnknown, time.Time{}
				m.pathTimer = nil
				m.bumpLocked()
			}
		})
	}
	if previous != path {
		m.bumpLocked()
	}
}
