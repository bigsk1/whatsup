package storage

// ForgetCommittedContact removes one exact approved association and its local
// conversation in the same encrypted snapshot. Device identity, unrelated chat
// scopes and the OS wrapping credential are preserved. This is a trusted backend
// operation, never a Wails binding: the caller must serialize its own lifecycle
// and stop/join every runtime user in stop before deletion can become durable.
//
// Unlike RemoveCommittedContact, this path accepts an already consumed pairing
// receipt. A cancellation claim first prevents new activation/local approval;
// stop runs outside the vault lock so joining a writer cannot deadlock. Exact
// record and receipt ownership are checked again before saving. Failed stopping
// or persistence leaves the association inert in this open state, for explicit
// close/reopen reconciliation, and never reports successful deletion.
func (a *ApplicationState) ForgetCommittedContact(record PendingRecord, stop func() error) error {
	if a == nil || a.vault == nil || record.Version != 2 || ValidatePendingRecord(record) != nil || stop == nil {
		return ErrCommitted
	}
	a.vault.mu.Lock()
	if err := a.usableLocked(); err != nil {
		a.vault.mu.Unlock()
		return err
	}
	if !a.hasCommittedLocked(record) {
		a.vault.mu.Unlock()
		return ErrCommitted
	}
	state := a.commitReceipts[record.ID]
	if state != nil && (state.record != record || state.canceling) {
		a.vault.mu.Unlock()
		return ErrCommittedUsed
	}
	if state == nil {
		a.nextCommitGeneration++
		state = &commitReceiptState{application: a, record: record, generation: a.nextCommitGeneration}
		if a.commitReceipts == nil {
			a.commitReceipts = make(map[string]*commitReceiptState)
		}
		a.commitReceipts[record.ID] = state
	}
	state.canceling = true
	delete(a.localReceipts, record.ID)
	if a.attachments != nil {
		for _, contact := range a.state.Contacts {
			if contact.ContactID == record.ID {
				a.attachments.RetireScope(contact.StoreID)
			}
		}
	}
	a.vault.mu.Unlock()

	if err := stop(); err != nil {
		// A failed runtime join cannot establish that every old writer stopped.
		// Poison this open coordinator so stale chat handles cannot keep saving
		// through it while the owner closes/reopens the authenticated snapshot.
		a.vault.mu.Lock()
		a.failed = true
		a.vault.mu.Unlock()
		return ErrCommitted
	}

	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	receipt := CommitReceipt{state: state}
	if err := receipt.validateStoredLocked(record); err != nil {
		return err
	}
	for index, committed := range a.state.CommittedContacts {
		if committed.ID != record.ID {
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
		if err := a.finishMessageDeletesLocked(); err != nil {
			return err
		}
		delete(a.commitReceipts, record.ID)
		delete(a.localReceipts, record.ID)
		return nil
	}
	return ErrCommitted
}
