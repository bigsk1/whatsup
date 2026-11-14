package transfer

type RelayApprovalRepository interface{ AllowRelayedLarge(string) error }

func (e *Engine) Queue(id string, allowRelayedLarge bool) error {
	return e.request(event{kind: actionEvent, action: func() error {
		record, err := e.repo.Get(id)
		if err != nil || record.Direction != Outgoing || record.Offer.Validate() != nil {
			return ErrUnavailable
		}
		if record.Offer.Size > LargeFileBytes && !allowRelayedLarge && !record.AllowRelayedLarge && !e.allowed(record) {
			return ErrRelayApproval
		}
		if err := e.repo.QueueOutgoing(id, allowRelayedLarge); err != nil {
			return err
		}
		e.drop(id)
		delete(e.jobs, id)
		e.changed(false)
		return e.reload()
	}})
}
func (e *Engine) Accept(id string) error {
	return e.request(event{kind: actionEvent, action: func() error {
		record, err := e.repo.Get(id)
		if err != nil || record.Direction != Incoming || record.State != Offered {
			return ErrUnavailable
		}
		if _, err = e.repo.Accept(id); err != nil {
			return err
		}
		e.changed(false)
		return e.promoteReceiver()
	}})
}
func (e *Engine) Decline(id string) error {
	return e.request(event{kind: actionEvent, action: func() error {
		record, err := e.repo.Get(id)
		if err != nil || record.Direction != Incoming {
			return ErrUnavailable
		}
		if e.finalDone != nil && e.activeIn == id {
			e.finalCancel()
			<-e.finalDone
			e.finalCancel, e.finalDone = nil, nil
		}
		if err = e.repo.Decline(id); err != nil {
			return err
		}
		e.drop(id)
		e.changed(false)
		if err = e.emit(control(DeclineFrame, record.Offer, 0, ReasonDeclined)); err != nil {
			return err
		}
		return e.promoteReceiver()
	}})
}
func (e *Engine) Cancel(id string) error {
	return e.request(event{kind: actionEvent, action: func() error {
		record, err := e.repo.Get(id)
		if err != nil {
			return ErrUnavailable
		}
		if e.finalDone != nil && e.activeIn == id {
			e.finalCancel()
			<-e.finalDone
			e.finalCancel, e.finalDone = nil, nil
		}
		if err = e.repo.Cancel(id); err != nil {
			return err
		}
		e.drop(id)
		e.changed(false)
		if err = e.emit(control(PauseFrame, record.Offer, 0, ReasonPaused)); err != nil {
			return err
		}
		return e.promoteReceiver()
	}})
}
func (e *Engine) Resume(id string) error {
	return e.request(event{kind: actionEvent, action: func() error {
		record, err := e.repo.Get(id)
		peerPaused := e.peerPaused[id]
		if job := e.jobs[id]; job != nil && job.remotePaused {
			peerPaused = true
		}
		if err != nil || record.State != Paused && !(peerPaused && (record.State == Accepted || record.State == Queued)) {
			return ErrUnavailable
		}
		if record.Direction == Outgoing && record.Offer.Size > LargeFileBytes && !record.AllowRelayedLarge && !e.allowed(record) {
			return ErrRelayApproval
		}
		if _, err = e.repo.Resume(id); err != nil {
			return err
		}
		delete(e.peerPaused, id)
		e.drop(id)
		delete(e.jobs, id)
		e.changed(false)
		if err = e.reload(); err != nil {
			return err
		}
		return e.promoteReceiver()
	}})
}
func (e *Engine) AllowRelay(id string) error {
	return e.request(event{kind: actionEvent, action: func() error {
		record, err := e.repo.Get(id)
		if err != nil || record.Direction != Outgoing {
			return ErrUnavailable
		}
		approval, ok := e.repo.(RelayApprovalRepository)
		if !ok {
			return ErrUnavailable
		}
		if err = approval.AllowRelayedLarge(id); err != nil {
			return err
		}
		e.changed(false)
		return e.reload()
	}})
}
func (e *Engine) Summaries() ([]Summary, error) {
	var values []Summary
	err := e.request(event{kind: actionEvent, action: func() error {
		records, err := e.repo.List()
		if err != nil {
			return err
		}
		values = make([]Summary, 0, len(records))
		for _, r := range records {
			if r.Hidden {
				continue
			}
			if !validRecord(r) {
				return ErrUnavailable
			}
			state := r.State
			next := r.NextChunk
			warning := ""
			if r.Direction == Incoming && e.peerPaused[r.Offer.ID] && state == Accepted {
				state = Paused
			}
			if r.Direction == Outgoing {
				if job := e.jobs[r.Offer.ID]; job != nil {
					next = job.acked
					if job.remotePaused && state == Queued {
						state = Paused
					}
				}
				if r.State != Draft && r.State != Complete && r.State != Declined && !e.allowed(r) {
					warning = "relay-approval"
				}
			}
			progress := uint64(next) * ChunkBytes
			if progress > r.Offer.Size {
				progress = r.Offer.Size
			}
			values = append(values, Summary{ID: r.Offer.ID, Name: r.Offer.Name, MediaType: r.Offer.MediaType, Size: r.Offer.Size, Direction: r.Direction, State: state, TransferredBytes: progress, Warning: warning})
		}
		return nil
	}})
	return values, err
}
