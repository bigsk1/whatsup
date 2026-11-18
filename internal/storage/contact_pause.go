package storage

// ContactPaused reads the local connection preference for one approved contact.
func (a *ApplicationState) ContactPaused(id string) (bool, error) {
	if a == nil || a.vault == nil {
		return false, ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	index, _, err := a.nameContactLocked(id)
	if err != nil {
		return false, err
	}
	return a.state.Contacts[index].Paused, nil
}

// SetContactPaused changes only the encrypted local preference. The registry
// serializes the corresponding connection change and joins the retired workers.
func (a *ApplicationState) SetContactPaused(id string, paused bool) error {
	if a == nil || a.vault == nil {
		return ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	index, _, err := a.nameContactLocked(id)
	if err != nil {
		return err
	}
	if a.state.Contacts[index].Paused == paused {
		return nil
	}
	next := a.cloneLocked()
	next.Contacts[index].Paused = paused
	return a.commitLocked(next)
}
