package messaging

import (
	"time"

	"whatsup/internal/attachmentstore"
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
	if m.peerRemoved || m.removing || m.sessionActive && !m.capabilities.Attachments {
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
	m, err := r.manager(contactID)
	if err != nil {
		return nil, err
	}
	scope, e, _, err := m.localAttachments(contactID)
	if err != nil {
		return nil, err
	}
	if e != nil {
		return e.Summaries()
	}
	records, err := scope.List()
	if err != nil {
		return nil, err
	}
	values := make([]transfer.Summary, 0, len(records))
	for _, record := range records {
		if record.Hidden {
			continue
		}
		if record.Offer.Validate() != nil || record.NextChunk > record.Offer.Chunks() {
			return nil, ErrUnavailable
		}
		progress := uint64(record.NextChunk) * transfer.ChunkBytes
		if progress > record.Offer.Size {
			progress = record.Offer.Size
		}
		warning := ""
		m.mu.Lock()
		direct := m.pathLocked(time.Now()) == transport.PathDirect
		m.mu.Unlock()
		if record.Direction == transfer.Outgoing && record.State != transfer.Draft && record.State != transfer.Complete && record.State != transfer.Declined && record.Offer.Size > transfer.LargeFileBytes && !record.AllowRelayedLarge && !direct {
			warning = "relay-approval"
		}
		values = append(values, transfer.Summary{ID: record.Offer.ID, Name: record.Offer.Name, MediaType: record.Offer.MediaType, Size: record.Offer.Size, Direction: record.Direction, State: record.State, TransferredBytes: progress, Warning: warning})
	}
	return values, nil
}

func (m *Manager) localAttachments(id string) (*attachmentstore.Scope, *transfer.Engine, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocked() != nil || m.record.ID != id || m.conversation == nil || m.state == "failed" {
		return nil, nil, false, ErrUnavailable
	}
	scope, err := m.application.AttachmentStore(id)
	if err != nil {
		return nil, nil, false, err
	}
	blocked := m.peerRemoved || m.removing || m.sessionActive && !m.capabilities.Attachments
	return scope, m.transfers, blocked, nil
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
	m, err := r.manager(contactID)
	if err != nil {
		return err
	}
	scope, e, _, err := m.localAttachments(contactID)
	if err != nil {
		return err
	}
	if e != nil {
		err = e.Decline(id)
	} else {
		err = scope.Decline(id)
	}
	if err == nil {
		r.MetadataChanged(contactID)
	}
	return err
}
func (r *Registry) CancelTransfer(contactID, id string) error {
	m, err := r.manager(contactID)
	if err != nil {
		return err
	}
	scope, e, _, err := m.localAttachments(contactID)
	if err != nil {
		return err
	}
	if e != nil {
		err = e.Cancel(id)
	} else {
		err = scope.Cancel(id)
	}
	if err == nil {
		r.MetadataChanged(contactID)
	}
	return err
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
	m, err := r.manager(contactID)
	if err != nil {
		return err
	}
	_, e, blocked, err := m.localAttachments(contactID)
	if err != nil {
		return err
	}
	if e != nil && !blocked {
		e.Wake()
	}
	// A local draft or preference change must refresh this contact even when
	// there is no connected session and no network-visible work to announce.
	r.MetadataChanged(contactID)
	return nil
}
