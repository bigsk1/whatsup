package transfer

import "sync"

// A permit belongs to one queued sender/credit generation. The scheduler may
// already hold a frame outside the actor when deletion retires that generation.
// Holding this small guard only through the bounded wire write makes successful
// deletion a barrier for that frame, without retaining every historical ID.
type outboundPermit struct {
	mu      sync.Mutex
	retired bool
}

func (p *outboundPermit) retire() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.retired = true
	p.mu.Unlock()
}

func outgoingContent(kind Kind) bool {
	return kind == OfferFrame || kind == ChunkFrame || kind == FinishFrame
}

func (e *Engine) outbound(frame Frame) Outbound {
	out := Outbound{Epoch: e.epoch, Frame: frame}
	if outgoingContent(frame.Kind) {
		if job := e.jobs[frame.ID]; job != nil {
			out.permit = job.permit
		}
	}
	return out
}

// WriteIfCurrent is Go-only scheduler authority. Terminal receipts remain
// writable after a local delete; they conclude obligations without file bytes.
// The callback must perform only the deadline-bounded write, never actor work.
func (out Outbound) WriteIfCurrent(write func() error) (bool, error) {
	if write == nil {
		return false, ErrUnavailable
	}
	if outgoingContent(out.Frame.Kind) {
		if out.permit == nil {
			return false, nil
		}
		out.permit.mu.Lock()
		defer out.permit.mu.Unlock()
		if out.permit.retired {
			return false, nil
		}
	}
	return true, write()
}

type removalRepository interface{ Remove(string) error }

// Remove deletes one local attachment, retaining the encrypted terminal record
// needed for authenticated receipts, withdrawal retries and replay rejection.
func (e *Engine) Remove(id string) error {
	if e == nil || !ValidID(id) {
		return ErrUnavailable
	}
	unblock := e.blockSources(id)
	defer unblock()
	return e.request(event{kind: actionEvent, action: func() error {
		remove, ok := e.repo.(removalRepository)
		if !ok {
			return ErrUnavailable
		}
		record, err := e.repo.Get(id)
		if err != nil || !validRecord(record) {
			return ErrUnavailable
		}
		if e.finalDone != nil && e.activeIn == id {
			e.finalCancel()
			<-e.finalDone
			e.finalCancel, e.finalDone = nil, nil
		}
		// Finalize may have durably completed before observing cancellation.
		record, err = e.repo.Get(id)
		if err != nil {
			return err
		}
		removeErr := remove.Remove(id)
		if removeErr != nil {
			// Object cleanup can fail after the hidden terminal record committed.
			// That record still revokes cached outbound frames immediately.
			current, readErr := e.repo.Get(id)
			if readErr != nil {
				e.protocolFailure(removeErr)
				return removeErr
			}
			if !current.Hidden {
				return removeErr
			}
		}
		var terminal []Frame
		for _, frame := range e.pending {
			if frame.ID == id && terminalReceipt(frame.Kind) {
				terminal = append(terminal, frame)
			}
		}
		select {
		case out := <-e.output:
			if out.Epoch == e.epoch && out.Frame.ID == id && terminalReceipt(out.Frame.Kind) {
				terminal = append(terminal, out.Frame)
			} else if out.Epoch == e.epoch && out.Frame.ID != id {
				e.pending = append([]Frame{out.Frame}, e.pending...)
			}
		default:
		}
		e.drop(id)
		delete(e.jobs, id)
		delete(e.withdrawSent, id)
		delete(e.peerPaused, id)
		completed := false
		for _, frame := range terminal {
			completed = completed || frame.Kind == CompleteFrame
			if err := e.emit(frame); err != nil {
				return err
			}
		}
		if record.Direction == Incoming && e.peerOffers[id] == record.Offer {
			if record.State == Complete && !completed {
				if err := e.emit(control(CompleteFrame, record.Offer, 0, 0)); err != nil {
					return err
				}
			} else if record.State != Complete && !e.withdrawals {
				if err := e.emit(control(DeclineFrame, record.Offer, 0, ReasonDeclined)); err != nil {
					return err
				}
			}
		}
		e.changed(false)
		if err := e.reload(); err != nil {
			return err
		}
		if err := e.promoteReceiver(); err != nil {
			return err
		}
		return removeErr
	}})
}
