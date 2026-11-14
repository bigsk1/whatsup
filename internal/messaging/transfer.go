package messaging

import (
	"time"

	"whatsup/internal/diagnostics"
	"whatsup/internal/transfer"
	"whatsup/internal/transport"
)

// attachmentEngine rechecks durable local approval before returning the exact
// contact-scoped capability. No caller may retain a scope across Service lock.
func (m *Manager) attachmentEngine(id string) (*transfer.Engine, error) {
	if m == nil || m.managerState == nil || !transfer.ValidID(id) {
		return nil, ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocked() != nil || m.record.ID != id || m.contact == nil || m.conversation == nil || m.state == "failed" {
		return nil, ErrUnavailable
	}
	if m.transfers != nil {
		return m.transfers, nil
	}
	repository, err := m.application.AttachmentStore(id)
	if err != nil {
		return nil, err
	}
	engine, err := transfer.New(repository, transfer.Options{
		Changed: func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if !m.closed {
				m.bumpLocked()
			}
		},
		Failed: func() { m.report(diagnostics.TransferFailed) },
		MaySend: func(transfer.Record) bool {
			m.mu.Lock()
			defer m.mu.Unlock()
			// This is a bandwidth warning based on a sampled status, not a
			// packet-boundary promise that a direct path can never use DERP.
			return m.pathLocked(time.Now()) == transport.PathDirect
		},
	})
	if err != nil {
		return nil, err
	}
	m.transfers = engine
	return engine, nil
}

func (r *Registry) transferEngine(contactID string) (*transfer.Engine, error) {
	m, err := r.manager(contactID)
	if err != nil {
		return nil, err
	}
	return m.attachmentEngine(contactID)
}

func (r *Registry) Transfers(contactID string) ([]transfer.Summary, error) {
	e, err := r.transferEngine(contactID)
	if err != nil {
		return nil, err
	}
	return e.Summaries()
}
func (r *Registry) QueueTransfer(contactID, id string, allowRelayedLarge bool) error {
	e, err := r.transferEngine(contactID)
	if err != nil {
		return err
	}
	return e.Queue(id, allowRelayedLarge)
}
func (r *Registry) AcceptTransfer(contactID, id string) error {
	e, err := r.transferEngine(contactID)
	if err != nil {
		return err
	}
	return e.Accept(id)
}
func (r *Registry) DeclineTransfer(contactID, id string) error {
	e, err := r.transferEngine(contactID)
	if err != nil {
		return err
	}
	return e.Decline(id)
}
func (r *Registry) CancelTransfer(contactID, id string) error {
	e, err := r.transferEngine(contactID)
	if err != nil {
		return err
	}
	return e.Cancel(id)
}
func (r *Registry) ResumeTransfer(contactID, id string) error {
	e, err := r.transferEngine(contactID)
	if err != nil {
		return err
	}
	return e.Resume(id)
}
func (r *Registry) AllowTransferRelay(contactID, id string) error {
	e, err := r.transferEngine(contactID)
	if err != nil {
		return err
	}
	return e.AllowRelay(id)
}
func (r *Registry) WakeTransfers(contactID string) error {
	e, err := r.transferEngine(contactID)
	if err != nil {
		return err
	}
	e.Wake()
	// A local draft or preference change must refresh this contact even when
	// there is no connected session and no network-visible work to announce.
	r.MetadataChanged(contactID)
	return nil
}
