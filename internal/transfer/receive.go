package transfer

import (
	"context"
	"crypto/rand"
	"errors"
	"time"
)

type imageApproval interface {
	AutoAcceptImages() (bool, error)
	AcceptImage(string) (Progress, error)
}

func imageOffer(offer Offer) bool {
	if offer.Size > 25_000_000 {
		return false
	}
	switch offer.MediaType {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
		return true
	}
	return false
}

func (e *Engine) receive(frame Frame) error {
	if frame.Validate() != nil || !e.live() {
		return ErrProtocol
	}
	if frame.Kind == OfferFrame {
		return e.receiveOffer(frame.Offer)
	}
	record, err := e.repo.Get(frame.ID)
	// An offer and its immediate withdrawal can cross queued actor output. An
	// unknown terminal notice grants no authority and creates no record.
	if errors.Is(err, ErrUnavailable) && frame.Kind == DeclineFrame {
		return nil
	}
	if err != nil || !validRecord(record) || record.Offer.SHA256 != frame.SHA256 {
		return ErrProtocol
	}
	// Terminal local cancellation wins over stale credits, ACKs and chunks
	// already in flight. Their exact authenticated offer ID/hash is still pinned.
	if record.State == Declined && frame.Kind != DeclineFrame {
		if frame.Kind == AcceptFrame || frame.Kind == ChunkFrame || frame.Kind == FinishFrame {
			return e.emit(control(DeclineFrame, record.Offer, 0, ReasonDeclined))
		}
		return nil
	}
	switch frame.Kind {
	case AcceptFrame:
		job := e.jobs[frame.ID]
		if record.Direction == Outgoing && record.State == Paused {
			return e.emit(control(PauseFrame, record.Offer, 0, ReasonPaused))
		}
		if record.Direction != Outgoing || record.State != Queued || job == nil || !job.offered || !checkpoint(record.Offer, frame.NextChunk) {
			return ErrProtocol
		}
		if job.accepted && !job.remotePaused && job.credit == frame.Credit {
			if frame.NextChunk != job.acked {
				return ErrProtocol
			}
			return nil
		}
		e.closeReader(job)
		job.record = record
		job.credit = frame.Credit
		job.accepted = true
		job.remotePaused = false
		job.blockedRelay = false
		job.next, job.acked = frame.NextChunk, frame.NextChunk
		job.finishSent, job.waitingWrite = false, false
		e.changed(false)
	case CheckpointFrame:
		job := e.jobs[frame.ID]
		if record.Direction == Outgoing && record.State == Paused {
			return nil
		}
		if job != nil && frame.Credit != job.credit {
			return nil
		}
		if record.Direction != Outgoing || job == nil || !job.accepted || !checkpoint(record.Offer, frame.NextChunk) || frame.NextChunk < job.acked || frame.NextChunk > job.next {
			return ErrProtocol
		}
		job.acked = frame.NextChunk
		e.changed(true)
	case ChunkFrame:
		if record.Direction != Incoming {
			return ErrProtocol
		}
		if record.State == Paused {
			return e.emit(control(PauseFrame, record.Offer, 0, ReasonPaused))
		}
		if record.State == Declined {
			return e.emit(control(DeclineFrame, record.Offer, 0, ReasonDeclined))
		}
		if record.State == Complete {
			return e.emit(control(CompleteFrame, record.Offer, 0, 0))
		}
		if record.State == Accepted && frame.Credit != e.credit {
			return nil
		}
		if record.State != Accepted || e.activeIn != frame.ID || e.finalDone != nil || frame.Index != e.expected {
			return ErrProtocol
		}
		size, err := record.Offer.ChunkSize(frame.Index)
		if err != nil || len(frame.Data) != size {
			return ErrProtocol
		}
		progress, err := e.repo.Append(frame.ID, frame.Index, frame.Data)
		if err != nil {
			return e.fileFailure(record.Offer, err)
		}
		if !checkpoint(record.Offer, progress.NextChunk) || progress.NextChunk < e.durable || progress.NextChunk > frame.Index+1 {
			return ErrProtocol
		}
		e.expected++
		if progress.NextChunk > e.durable {
			e.durable = progress.NextChunk
			e.changed(true)
			return e.emit(creditControl(CheckpointFrame, record.Offer, e.durable, e.credit))
		}
	case FinishFrame:
		if record.Direction != Incoming {
			return ErrProtocol
		}
		if record.State == Complete {
			return e.emit(control(CompleteFrame, record.Offer, 0, 0))
		}
		if record.State == Paused {
			return e.emit(control(PauseFrame, record.Offer, 0, ReasonPaused))
		}
		if record.State == Declined {
			return e.emit(control(DeclineFrame, record.Offer, 0, ReasonDeclined))
		}
		if record.State == Accepted && frame.Credit != e.credit {
			return nil
		}
		if record.State != Accepted || e.activeIn != frame.ID || record.NextChunk != record.Offer.Chunks() || e.expected != record.NextChunk {
			return ErrProtocol
		}
		if e.finalDone != nil {
			return nil
		}
		ctx, cancel := context.WithCancel(e.session)
		done := make(chan struct{})
		e.finalCancel, e.finalDone = cancel, done
		epoch, id, hash := e.epoch, frame.ID, frame.SHA256
		go func() {
			defer close(done)
			err := e.repo.Finalize(ctx, id)
			select {
			case e.finalized <- finalResult{epoch, id, hash, err}:
			case <-ctx.Done():
			case <-e.ctx.Done():
			}
		}()
	case CompleteFrame:
		if record.Direction != Outgoing {
			return ErrProtocol
		}
		if record.State == Complete {
			return nil
		}
		job := e.jobs[frame.ID]
		if job == nil || !job.offered {
			return ErrProtocol
		}
		if err := e.repo.MarkDelivered(frame.ID); err != nil {
			return e.fileFailure(record.Offer, err)
		}
		e.drop(frame.ID)
		delete(e.jobs, frame.ID)
		e.changed(false)
	case PauseFrame, FailureFrame:
		if record.State == Complete || record.State == Declined {
			return nil
		}
		if record.Direction == Incoming {
			if e.peerOffers[frame.ID] != record.Offer {
				return ErrProtocol
			}
			e.peerPaused[frame.ID] = true
		} else if job := e.jobs[frame.ID]; job == nil || !job.offered {
			return ErrProtocol
		}
		if e.finalDone != nil && e.activeIn == frame.ID {
			e.finalCancel()
			<-e.finalDone
			e.finalCancel, e.finalDone = nil, nil
		}
		e.drop(frame.ID)
		if frame.Kind == FailureFrame {
			e.failed()
		}
		e.changed(false)
		return e.promoteReceiver()
	case DeclineFrame:
		if record.State == Complete || record.State == Declined {
			return nil
		}
		if record.Direction == Incoming {
			if e.peerOffers[frame.ID] != record.Offer {
				return ErrProtocol
			}
		} else if job := e.jobs[frame.ID]; job == nil || !job.offered {
			return ErrProtocol
		}
		if e.finalDone != nil && e.activeIn == frame.ID {
			e.finalCancel()
			<-e.finalDone
			e.finalCancel, e.finalDone = nil, nil
		}
		if err := e.repo.Decline(frame.ID); err != nil {
			return err
		}
		e.drop(frame.ID)
		delete(e.jobs, frame.ID)
		e.changed(false)
		return e.promoteReceiver()
	default:
		return ErrProtocol
	}
	return nil
}

func (e *Engine) receiveOffer(offer Offer) error {
	now := time.Now()
	if now.Sub(e.offerAt) >= 30*time.Second {
		e.offerTokens = 32
		e.offerAt = now
	}
	if e.offerTokens == 0 {
		return ErrProtocol
	}
	e.offerTokens--
	if err := e.repo.Offer(offer); err != nil {
		if errors.Is(err, ErrConflict) {
			return ErrProtocol
		}
		e.failed()
		reason := ReasonUnavailable
		if errors.Is(err, ErrCapacity) {
			reason = ReasonCapacity
		}
		return e.emit(control(FailureFrame, offer, 0, reason))
	}
	record, err := e.repo.Get(offer.ID)
	if err != nil || record.Direction != Incoming || record.Offer != offer {
		return ErrProtocol
	}
	_, known := e.peerOffers[offer.ID]
	if !known && len(e.peerOffers) >= MaxOffers+MaxActive {
		return ErrProtocol
	}
	e.peerOffers[offer.ID] = offer
	delete(e.peerPaused, offer.ID)
	if record.State == Offered {
		if approval, ok := e.repo.(imageApproval); ok && imageOffer(offer) {
			allowed, err := approval.AutoAcceptImages()
			if err == nil && allowed {
				if _, err = approval.AcceptImage(offer.ID); err != nil {
					return e.fileFailure(offer, err)
				}
				record, err = e.repo.Get(offer.ID)
				if err != nil {
					return err
				}
			}
		}
	}
	if !known {
		e.changed(false)
	}
	switch record.State {
	case Offered:
		return nil
	case Accepted:
		if e.activeIn == offer.ID {
			if e.finalDone != nil {
				return nil
			}
			e.activeIn = ""
		}
		if e.activeIn != "" {
			return e.emit(control(PauseFrame, offer, 0, ReasonUnavailable))
		}
		return e.promoteReceiver()
	case Paused:
		return e.emit(control(PauseFrame, offer, 0, ReasonPaused))
	case Complete:
		return e.emit(control(CompleteFrame, offer, 0, 0))
	case Declined:
		return e.emit(control(DeclineFrame, offer, 0, ReasonDeclined))
	default:
		return ErrProtocol
	}
}

func (e *Engine) promoteReceiver() error {
	if e.activeIn != "" || !e.live() {
		return nil
	}
	for id, offer := range e.peerOffers {
		record, err := e.repo.Get(id)
		if err != nil {
			return err
		}
		if record.Direction != Incoming || record.Offer != offer {
			return ErrProtocol
		}
		if record.State != Accepted || e.peerPaused[id] {
			continue
		}
		progress, err := e.repo.Resume(id)
		if err != nil {
			return e.fileFailure(offer, err)
		}
		if !checkpoint(offer, progress.NextChunk) {
			return ErrProtocol
		}
		e.activeIn = id
		e.expected, e.durable = progress.NextChunk, progress.NextChunk
		if _, err := rand.Read(e.credit[:]); err != nil || e.credit == [16]byte{} {
			return ErrUnavailable
		}
		return e.emit(creditControl(AcceptFrame, offer, progress.NextChunk, e.credit))
	}
	return nil
}

func (e *Engine) finalResult(result finalResult) {
	if result.epoch != e.epoch || result.id != e.activeIn {
		return
	}
	if e.finalDone != nil {
		<-e.finalDone
	}
	e.finalCancel, e.finalDone = nil, nil
	if !e.live() {
		return
	}
	offer := e.peerOffers[result.id]
	if result.err != nil {
		if errors.Is(result.err, context.Canceled) {
			return
		}
		if errors.Is(result.err, ErrIntegrity) {
			_ = e.repo.Decline(result.id)
		}
		_ = e.fileFailure(offer, result.err)
	} else {
		e.activeIn = ""
		e.expected, e.durable = 0, 0
		e.changed(false)
		if err := e.emit(control(CompleteFrame, offer, 0, 0)); err != nil {
			e.protocolFailure(err)
			return
		}
	}
	if err := e.promoteReceiver(); err != nil {
		e.protocolFailure(err)
	}
}
