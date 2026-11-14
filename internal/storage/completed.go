package storage

import "errors"

const MaxCommittedContacts = 32

var (
	ErrCommitted         = errors.New("committed contact record is unavailable or invalid")
	ErrCommittedConflict = errors.New("committed contact conflicts with an existing conversation")
	ErrCommittedUsed     = errors.New("durable contact receipt was already consumed")
	ErrCommittedRestore  = errors.New("restored contact requires fresh authenticated reconfirmation")
)

// CommitReceipt proves a successful durable write through this open coordinator.
// It is deliberately absent after restore. The encrypted disk record alone is
// not a live approval or transport capability, even if it was committed earlier.
type CommitReceipt struct{ state *commitReceiptState }

type commitReceiptState struct {
	application *ApplicationState
	record      PendingRecord
	generation  uint64
	consumed    bool
	canceling   bool
}

func (CommitReceipt) String() string               { return "[private WhatsUp durable contact receipt]" }
func (CommitReceipt) GoString() string             { return "[private WhatsUp durable contact receipt]" }
func (CommitReceipt) MarshalJSON() ([]byte, error) { return nil, ErrCommitted }

// CommitPendingContact moves exactly the validated pending bundle, never minting
// another key or PSK and never creating a chat pin. Existing legacy/chat scopes
// for that peer are rejected so a new pairing cannot inherit previous history.
// Identical retries in this open coordinator share the same one-shot receipt.
func (a *ApplicationState) CommitPendingContact(record PendingRecord) (*CommitReceipt, error) {
	if a == nil || a.vault == nil || ValidatePendingRecord(record) != nil {
		return nil, ErrCommitted
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return nil, err
	}
	for _, existing := range a.state.CommittedContacts {
		if existing.ID == record.ID {
			if PendingRecord(existing) != record {
				return nil, ErrCommittedConflict
			}
			state := a.commitReceipts[record.ID]
			if state == nil {
				return nil, ErrCommittedRestore
			}
			if state.consumed || state.canceling {
				return nil, ErrCommittedUsed
			}
			return &CommitReceipt{state: state}, nil
		}
		if existing.Peer.Equal(record.Peer) {
			return nil, ErrCommittedConflict
		}
	}
	if a.findContactLocked(record.Local, record.Peer) >= 0 {
		return nil, ErrCommittedConflict
	}
	if len(a.state.Contacts) >= MaxContacts {
		return nil, ErrCapacity
	}
	pendingIndex := -1
	for index, pending := range a.state.PendingContacts {
		if pending.ID == record.ID {
			if PendingRecord(pending) != record {
				return nil, ErrCommittedConflict
			}
			pendingIndex = index
			break
		}
	}
	if pendingIndex < 0 {
		return nil, ErrCommitted
	}
	if len(a.state.CommittedContacts) >= MaxCommittedContacts {
		return nil, ErrCapacity
	}
	next := a.cloneLocked()
	next.CommittedContacts = append(next.CommittedContacts, pendingRecordDisk(record))
	copy(next.PendingContacts[pendingIndex:], next.PendingContacts[pendingIndex+1:])
	clear(next.PendingContacts[len(next.PendingContacts)-1:])
	next.PendingContacts = next.PendingContacts[:len(next.PendingContacts)-1]
	if err := a.commitLocked(next); err != nil {
		return nil, err
	}
	a.nextCommitGeneration++
	state := &commitReceiptState{application: a, record: record, generation: a.nextCommitGeneration}
	if a.commitReceipts == nil {
		a.commitReceipts = make(map[string]*commitReceiptState)
	}
	a.commitReceipts[record.ID] = state
	return &CommitReceipt{state: state}, nil
}

// CommittedContacts returns only inert backend record copies. A future runtime
// must freshly authenticate the exact peer and association before restoring live
// authority; this getter cannot produce a CommitReceipt.
func (a *ApplicationState) CommittedContacts() ([]PendingRecord, error) {
	if a == nil || a.vault == nil {
		return nil, ErrCommitted
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return nil, err
	}
	records := make([]PendingRecord, len(a.state.CommittedContacts))
	for index, record := range a.state.CommittedContacts {
		records[index] = PendingRecord(record)
	}
	return records, nil
}

// validateLocked requires the owning vault mutex. Map identity plus generation
// invalidates every stale copy after remove/recommit, close, or an uncertain save.
func (r *CommitReceipt) validateLocked(record PendingRecord) error {
	if err := r.validateStoredLocked(record); err != nil {
		return err
	}
	if r.state.consumed || r.state.canceling {
		return ErrCommittedUsed
	}
	return nil
}

// validateStoredLocked checks exact ownership even after consumption. The caller
// must separately decide whether a consuming or canceling operation is allowed.
func (r *CommitReceipt) validateStoredLocked(record PendingRecord) error {
	state := r.state
	a := state.application
	if err := a.usableLocked(); err != nil {
		return err
	}
	current := a.commitReceipts[state.record.ID]
	if current != state || current.generation != state.generation || state.record != record {
		return ErrCommitted
	}
	for _, committed := range a.state.CommittedContacts {
		if committed.ID == state.record.ID && PendingRecord(committed) == record {
			return nil
		}
	}
	return ErrCommitted
}

func (r *CommitReceipt) validState() bool {
	return r != nil && r.state != nil && r.state.application != nil && r.state.application.vault != nil
}

// ValidateRecord is a bounded in-memory check. It performs no disk IO and may
// be called from a channel completion guard; it never calls channel/manager code.
func (r *CommitReceipt) ValidateRecord(record PendingRecord) error {
	if !r.validState() {
		return ErrCommitted
	}
	a := r.state.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	return r.validateLocked(record)
}

// RecordForActivation exposes a backend-only copy for exact association checks,
// never a frontend DTO or a permission to activate without channel completion.
func (r *CommitReceipt) RecordForActivation() (PendingRecord, error) {
	if !r.validState() {
		return PendingRecord{}, ErrCommitted
	}
	a := r.state.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := r.validateLocked(r.state.record); err != nil {
		return PendingRecord{}, err
	}
	return r.state.record, nil
}

func (r *CommitReceipt) ConsumeForActivation(record PendingRecord) error {
	if !r.validState() {
		return ErrCommitted
	}
	a := r.state.application
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := r.validateLocked(record); err != nil {
		return err
	}
	r.state.consumed = true
	return nil
}

// CancelAfterRevocation is the explicit cancellation path for a receipt whose
// runtime authority may already have been issued. The trusted backend callback
// must revoke/drain its own exact Contact and credentials. It runs without the
// vault mutex. Claiming cancellation first blocks every later receipt use;
// durable deletion occurs only after the callback returns. A failed/uncertain
// deletion leaves stopped runtime and inert state for reopen/reconciliation.
func (r *CommitReceipt) CancelAfterRevocation(revoke func()) error {
	if !r.validState() || revoke == nil {
		return ErrCommitted
	}
	a := r.state.application
	a.vault.mu.Lock()
	if err := r.validateStoredLocked(r.state.record); err != nil {
		a.vault.mu.Unlock()
		return err
	}
	if r.state.canceling {
		a.vault.mu.Unlock()
		return ErrCommittedUsed
	}
	r.state.canceling = true
	a.vault.mu.Unlock()

	revoke()

	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := r.validateStoredLocked(r.state.record); err != nil {
		return err
	}
	for index, record := range a.state.CommittedContacts {
		if record.ID != r.state.record.ID {
			continue
		}
		next := a.cloneLocked()
		copy(next.CommittedContacts[index:], next.CommittedContacts[index+1:])
		clear(next.CommittedContacts[len(next.CommittedContacts)-1:])
		next.CommittedContacts = next.CommittedContacts[:len(next.CommittedContacts)-1]
		a.removeConversationLocked(&next, record.ID)
		if err := a.commitLocked(next); err != nil {
			return err
		}
		delete(a.commitReceipts, record.ID)
		delete(a.localReceipts, record.ID)
		return nil
	}
	return ErrCommitted
}

// RemoveCommittedContact supports explicit discard of inert or canceled setup.
// It also deletes only this exact contact-ID conversation, if present; unrelated
// and legacy history is preserved. The owner must stop runtime users first. A
// consumed pairing receipt requires separate revocation orchestration here.
func (a *ApplicationState) RemoveCommittedContact(id string) error {
	if a == nil || a.vault == nil || !pendingIDValid(id) {
		return ErrCommitted
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return err
	}
	if receipt := a.commitReceipts[id]; receipt != nil && (receipt.consumed || receipt.canceling) {
		return ErrCommittedUsed
	}
	for index, record := range a.state.CommittedContacts {
		if record.ID != id {
			continue
		}
		next := a.cloneLocked()
		copy(next.CommittedContacts[index:], next.CommittedContacts[index+1:])
		clear(next.CommittedContacts[len(next.CommittedContacts)-1:])
		next.CommittedContacts = next.CommittedContacts[:len(next.CommittedContacts)-1]
		a.removeConversationLocked(&next, id)
		if err := a.commitLocked(next); err != nil {
			return err
		}
		delete(a.commitReceipts, id)
		delete(a.localReceipts, id)
		return nil
	}
	return nil
}
