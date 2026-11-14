package transfer

import "context"

// sourceContext and blockSources let a local cancellation interrupt a slow
// source without waiting for that source on the actor's own event queue. The
// block also covers an Open that would otherwise race the queued local action.
func (e *Engine) sourceContext(id string) (context.Context, bool) {
	e.readMu.Lock()
	defer e.readMu.Unlock()
	if e.blockedAllReads != 0 || e.blockedReads[id] != 0 {
		return nil, false
	}
	ctx, cancel := context.WithCancel(e.session)
	e.readCancels[id] = cancel
	return ctx, true
}

func (e *Engine) blockSources(id string) func() {
	e.readMu.Lock()
	if id == "" {
		e.blockedAllReads++
	} else {
		e.blockedReads[id]++
	}
	for current, cancel := range e.readCancels {
		if id == "" || current == id {
			cancel()
		}
	}
	e.readMu.Unlock()
	return func() {
		e.readMu.Lock()
		defer e.readMu.Unlock()
		if id == "" {
			e.blockedAllReads--
		} else {
			e.blockedReads[id]--
			if e.blockedReads[id] == 0 {
				delete(e.blockedReads, id)
			}
		}
	}
}

// Withdraw permanently cancels an unfinished transfer, unlike Cancel's existing
// pause/resume action. The durable terminal offer prevents reconnect or stale
// credit from re-creating a sender or retaining an active receiver slot.
func (e *Engine) Withdraw(id string) error {
	if e == nil || !ValidID(id) {
		return ErrUnavailable
	}
	unblock := e.blockSources(id)
	defer unblock()
	return e.request(event{kind: actionEvent, action: func() error {
		record, err := e.repo.Get(id)
		if err != nil || record.State == Complete {
			return ErrUnavailable
		}
		if e.finalDone != nil && e.activeIn == id {
			e.finalCancel()
			<-e.finalDone
			e.finalCancel, e.finalDone = nil, nil
		}
		if err := e.repo.Decline(id); err != nil {
			return err
		}
		e.drop(id)
		delete(e.jobs, id)
		delete(e.peerPaused, id)
		if record.State != Draft {
			if err := e.emit(control(DeclineFrame, record.Offer, 0, ReasonDeclined)); err != nil {
				return err
			}
		}
		e.changed(false)
		return e.promoteReceiver()
	}})
}

// ClearHistory serializes the durable history transaction with attachment IO.
// The callback must not call this Engine or retain the messaging mutex while
// waiting for this action. It atomically tombstones queued/received attachments
// together with history and reclaims their encrypted objects.
func (e *Engine) ClearHistory(clearHistory func() error) error {
	if e == nil || clearHistory == nil {
		return ErrUnavailable
	}
	unblock := e.blockSources("")
	defer unblock()
	return e.request(event{kind: actionEvent, action: func() error {
		records, err := e.repo.List()
		if err != nil {
			return err
		}
		session, guard := e.session, e.guard
		e.end() // Cancels/joins final verification and closes every source first.
		err = clearHistory()
		e.session, e.guard = session, guard
		if err != nil {
			_ = e.reload()
			return err
		}
		for _, record := range records {
			if record.State == Draft || record.State == Complete || record.State == Declined {
				continue
			}
			if err := e.emit(control(DeclineFrame, record.Offer, 0, ReasonDeclined)); err != nil {
				return err
			}
		}
		e.changed(false)
		return nil
	}})
}
