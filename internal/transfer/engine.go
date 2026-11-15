package transfer

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

type Options struct {
	Changed func()
	Failed  func()
	MaySend func(Record) bool
	// Now is a Go-only clock seam for offer-rate-limit tests. Production leaves
	// it nil and always uses the real monotonic clock.
	Now func() time.Time
}

type Outbound struct {
	Epoch uint64
	Frame Frame
}
type Failure struct {
	Epoch uint64
	Err   error
}
type event struct {
	kind   byte
	epoch  uint64
	frame  Frame
	ctx    context.Context
	guard  func() error
	action func() error
	reply  chan error
}

const (
	beginEvent byte = iota + 1
	endEvent
	incomingEvent
	writtenEvent
	actionEvent
	wakeEvent
)

// A receipt retires one durable intent before another enters the wire. This is
// deliberately smaller than the actor's bounded input queue, including when both
// directions are simultaneously replaying cancellations after a restart.
const maxWithdrawalsInFlight = 4

type withdrawalFlight struct {
	offer     Offer
	writtenAt time.Time
	queued    bool
	retries   uint8
}

type sendJob struct {
	credit                                                                               [16]byte
	record                                                                               Record
	offered, offerQueued, accepted, remotePaused, waitingWrite, finishSent, blockedRelay bool
	next, acked                                                                          uint32
	reader                                                                               io.ReadSeekCloser
}
type finalResult struct {
	epoch uint64
	id    string
	hash  [32]byte
	err   error
}

// Engine has one attachment IO worker. Its input holds at most32 bounded
// events, its output one frame, and only one file in each direction has credit.
// No file read, sync or whole-file verification runs on the TLS writer goroutine.
type Engine struct {
	repo              Repository
	options           Options
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	events            chan event
	output            chan Outbound
	failures          chan Failure
	closeOnce         sync.Once
	readMu            sync.Mutex
	readCancels       map[string]context.CancelFunc
	blockedReads      map[string]int
	blockedAllReads   int
	epoch             uint64
	session           context.Context
	guard             func() error
	pending           []Frame
	jobs              map[string]*sendJob
	order             []string
	activeOut         string
	peerOffers        map[string]Offer
	peerPaused        map[string]bool
	activeIn          string
	expected, durable uint32
	credit            [16]byte
	finalCancel       context.CancelFunc
	finalDone         chan struct{}
	finalized         chan finalResult
	offerTokens       int
	offerAt           time.Time
	progressAt        time.Time
	withdrawals       bool
	withdrawSent      map[string]*withdrawalFlight
}

func New(repo Repository, options Options) (*Engine, error) {
	if repo == nil {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{repo: repo, options: options, ctx: ctx, cancel: cancel, done: make(chan struct{}), events: make(chan event, 32), output: make(chan Outbound, 1), failures: make(chan Failure, 2), jobs: map[string]*sendJob{}, peerOffers: map[string]Offer{}, peerPaused: map[string]bool{}, finalized: make(chan finalResult, 1), offerTokens: 32, offerAt: time.Now(), readCancels: map[string]context.CancelFunc{}, blockedReads: map[string]int{}}
	e.offerAt = e.now()
	go e.run()
	return e, nil
}

func (e *Engine) Output() <-chan Outbound  { return e.output }
func (e *Engine) Failures() <-chan Failure { return e.failures }
func (e *Engine) request(ev event) error {
	if e == nil {
		return ErrUnavailable
	}
	ev.reply = make(chan error, 1)
	select {
	case <-e.ctx.Done():
		return ErrUnavailable
	case e.events <- ev:
	}
	select {
	case <-e.ctx.Done():
		return ErrUnavailable
	case err := <-ev.reply:
		return err
	}
}
func (e *Engine) Begin(ctx context.Context, guard func() error) (uint64, error) {
	return e.BeginWithWithdrawals(ctx, guard, true)
}

// BeginWithWithdrawals accepts only the capability obtained from the exact TLS
// session. Never infer new wire grammar from an application version or a file.
func (e *Engine) BeginWithWithdrawals(ctx context.Context, guard func() error, withdrawals bool) (uint64, error) {
	if ctx == nil || ctx.Err() != nil || guard == nil {
		return 0, ErrUnavailable
	}
	var epoch uint64
	err := e.request(event{kind: actionEvent, action: func() error {
		e.end()
		e.epoch++
		epoch = e.epoch
		e.session, e.guard = ctx, guard
		e.withdrawals = withdrawals
		return e.reload()
	}})
	return epoch, err
}
func (e *Engine) End(epoch uint64) error { return e.request(event{kind: endEvent, epoch: epoch}) }
func (e *Engine) Submit(epoch uint64, frame Frame) error {
	return e.SubmitContext(e.ctx, epoch, frame)
}

// SubmitContext applies bounded backpressure instead of treating a busy local
// persistence worker as a peer protocol violation. Session cancellation always
// interrupts the wait, even if this Engine remains alive for a later session.
func (e *Engine) SubmitContext(ctx context.Context, epoch uint64, frame Frame) error {
	if frame.Validate() != nil {
		return ErrProtocol
	}
	if ctx == nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.ctx.Done():
		return ErrUnavailable
	case e.events <- event{kind: incomingEvent, epoch: epoch, frame: frame}:
		return nil
	}
}
func (e *Engine) Written(epoch uint64, frame Frame) error {
	return e.WrittenContext(e.ctx, epoch, frame)
}
func (e *Engine) WrittenContext(ctx context.Context, epoch uint64, frame Frame) error {
	if ctx == nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.ctx.Done():
		return ErrUnavailable
	case e.events <- event{kind: writtenEvent, epoch: epoch, frame: frame}:
		return nil
	}
}
func (e *Engine) Wake() {
	if e == nil {
		return
	}
	select {
	case <-e.ctx.Done():
	case e.events <- event{kind: wakeEvent}:
	default:
	}
}
func (e *Engine) Close() {
	if e == nil {
		return
	}
	unblock := e.blockSources("")
	defer unblock()
	e.closeOnce.Do(e.cancel)
	<-e.done
}
func (e *Engine) live() bool {
	return e.session != nil && e.session.Err() == nil && e.guard != nil && e.guard() == nil
}
func (e *Engine) now() time.Time {
	if e.options.Now != nil {
		return e.options.Now()
	}
	return time.Now()
}
func (e *Engine) changed(progress bool) {
	if progress && time.Since(e.progressAt) < 250*time.Millisecond {
		return
	}
	e.progressAt = time.Now()
	if e.options.Changed != nil {
		e.options.Changed()
	}
}
func (e *Engine) failed() {
	if e.options.Failed != nil {
		e.options.Failed()
	}
}
func (e *Engine) emit(f Frame) error {
	if !e.live() {
		return nil
	}
	// Controls are small; clearing the bounded offer set may need one terminal
	// response per offer. Chunk data still has only one output slot.
	if f.Validate() != nil || len(e.pending) >= MaxOffers+MaxActive+4 {
		return ErrProtocol
	}
	e.pending = append(e.pending, f)
	return nil
}
func control(kind Kind, offer Offer, next uint32, reason byte) Frame {
	return Frame{Kind: kind, ID: offer.ID, SHA256: offer.SHA256, NextChunk: next, Reason: reason}
}
func creditControl(kind Kind, offer Offer, next uint32, credit [16]byte) Frame {
	f := control(kind, offer, next, 0)
	f.Credit = credit
	return f
}
func checkpoint(offer Offer, next uint32) bool {
	return next <= offer.Chunks() && (next%UnitChunks == 0 || next == offer.Chunks())
}
func (e *Engine) closeReader(job *sendJob) {
	if job == nil {
		return
	}
	e.readMu.Lock()
	if cancel := e.readCancels[job.record.Offer.ID]; cancel != nil {
		cancel()
		delete(e.readCancels, job.record.Offer.ID)
	}
	e.readMu.Unlock()
	if job.reader != nil {
		_ = job.reader.Close()
		job.reader = nil
	}
}
func (e *Engine) end() {
	if e.finalCancel != nil {
		e.finalCancel()
	}
	if e.finalDone != nil {
		<-e.finalDone
	}
	e.finalCancel, e.finalDone = nil, nil
	for _, job := range e.jobs {
		e.closeReader(job)
	}
	e.jobs = map[string]*sendJob{}
	e.order = nil
	e.session, e.guard = nil, nil
	e.pending = nil
	e.activeIn, e.activeOut = "", ""
	e.expected, e.durable = 0, 0
	e.credit = [16]byte{}
	e.peerOffers = map[string]Offer{}
	e.peerPaused = map[string]bool{}
	e.withdrawSent = map[string]*withdrawalFlight{}
	for {
		select {
		case <-e.output:
		default:
			goto drained
		}
	}
drained:
	for {
		select {
		case <-e.finalized:
		default:
			return
		}
	}
}
func (e *Engine) reload() error {
	records, err := e.repo.List()
	if err != nil {
		return err
	}
	if len(records) > 2*(MaxOffers+MaxActive+4) {
		return ErrUnavailable
	}
	seen := map[string]bool{}
	order := make([]string, 0, len(records))
	for _, r := range records {
		if !validRecord(r) || seen[r.Offer.ID] {
			return ErrUnavailable
		}
		seen[r.Offer.ID] = true
		if r.Direction != Outgoing {
			continue
		}
		if r.State != Queued {
			if j := e.jobs[r.Offer.ID]; j != nil {
				e.closeReader(j)
				j.record = r
				j.accepted = false
			}
			continue
		}
		job := e.jobs[r.Offer.ID]
		if job == nil {
			job = &sendJob{}
			e.jobs[r.Offer.ID] = job
		}
		job.record = r
		order = append(order, r.Offer.ID)
		if job.blockedRelay && e.allowed(r) {
			job.offered, job.offerQueued, job.remotePaused, job.blockedRelay = false, false, false, false
		}
	}
	for id, job := range e.jobs {
		if !seen[id] {
			e.closeReader(job)
			delete(e.jobs, id)
		}
	}
	e.order = order
	if !e.live() {
		return nil
	}
	for _, r := range records {
		if r.PendingWithdrawal && e.withdrawals && e.withdrawSent[r.Offer.ID] == nil {
			if err := e.queueWithdrawal(r); err != nil {
				return err
			}
		}
	}
	for _, id := range e.order {
		job := e.jobs[id]
		if job.offered || job.offerQueued {
			continue
		}
		if !e.allowed(job.record) {
			continue
		}
		if len(e.pending) >= MaxOffers+MaxActive+4 {
			break
		}
		if err := e.emit(Frame{Kind: OfferFrame, ID: id, SHA256: job.record.Offer.SHA256, Offer: job.record.Offer}); err != nil {
			return err
		}
		job.offerQueued = true
	}
	return nil
}

func (e *Engine) queueWithdrawal(record Record) error {
	if !e.withdrawals || !record.PendingWithdrawal || e.withdrawSent[record.Offer.ID] != nil || len(e.withdrawSent) >= maxWithdrawalsInFlight || len(e.pending) >= MaxOffers+MaxActive+4 {
		return nil
	}
	if err := e.emit(control(WithdrawFrame, record.Offer, 0, 0)); err != nil {
		return err
	}
	e.withdrawSent[record.Offer.ID] = &withdrawalFlight{offer: record.Offer, queued: true}
	return nil
}

func withdrawalRetryDelay(retries uint8) time.Duration {
	delay := 2 * time.Second << min(retries, 4)
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

// A TCP session may survive a peer losing a queued receipt, for example after
// its history is cleared. Retry only the existing four durable obligations,
// retaining their IDs/hashes and bounded exponential timing without reconnect.
func (e *Engine) retryWithdrawals() error {
	if !e.live() || !e.withdrawals {
		return nil
	}
	now := e.now()
	for _, flight := range e.withdrawSent {
		if flight.queued || flight.writtenAt.IsZero() || now.Sub(flight.writtenAt) < withdrawalRetryDelay(flight.retries) {
			continue
		}
		if len(e.pending) >= MaxOffers+MaxActive+4 {
			break
		}
		if err := e.emit(control(WithdrawFrame, flight.offer, 0, 0)); err != nil {
			return err
		}
		flight.queued = true
		if flight.retries < 4 {
			flight.retries++
		}
	}
	return nil
}
func (e *Engine) allowed(record Record) bool {
	return record.Offer.Size <= LargeFileBytes || record.AllowRelayedLarge || e.options.MaySend != nil && e.options.MaySend(record)
}

func (e *Engine) run() {
	defer close(e.done)
	defer e.end()
	retry := time.NewTicker(time.Second)
	defer retry.Stop()
	var staged *Frame
	for {
		if e.ctx.Err() != nil {
			return
		}
		if !e.live() {
			staged = nil
		}
		if staged != nil {
			job := e.jobs[staged.ID]
			if job == nil || !job.accepted || job.remotePaused || staged.Credit != job.credit {
				staged = nil
			}
		}
		if staged == nil && len(e.pending) == 0 && e.live() {
			f, ok, err := e.nextChunk()
			if err != nil {
				e.protocolFailure(err)
			} else if ok {
				staged = &f
			}
		}
		var out chan Outbound
		var value Outbound
		if e.live() {
			if len(e.pending) > 0 {
				out = e.output
				value = Outbound{e.epoch, e.pending[0]}
			} else if staged != nil {
				out = e.output
				value = Outbound{e.epoch, *staged}
			}
		}
		select {
		case <-e.ctx.Done():
			return
		case out <- value:
			if len(e.pending) > 0 {
				e.pending = e.pending[1:]
			} else {
				staged = nil
			}
		case result := <-e.finalized:
			e.finalResult(result)
		case <-retry.C:
			if err := e.retryWithdrawals(); err != nil {
				e.protocolFailure(err)
			}
		case ev := <-e.events:
			var err error
			switch ev.kind {
			case actionEvent:
				err = ev.action()
			case endEvent:
				if ev.epoch == e.epoch {
					e.end()
					staged = nil
				}
			case incomingEvent:
				if ev.epoch == e.epoch && e.live() {
					err = e.receive(ev.frame)
				}
			case writtenEvent:
				if ev.epoch == e.epoch && e.live() {
					err = e.written(ev.frame)
				}
			case wakeEvent:
				err = e.retryWithdrawals()
				if err == nil {
					err = e.reload()
				}
			}
			if ev.reply != nil {
				ev.reply <- err
			} else if err != nil {
				e.protocolFailure(err)
			}
		}
	}
}
func (e *Engine) protocolFailure(err error) {
	if e.live() {
		select {
		case e.failures <- Failure{e.epoch, err}:
		default:
		}
	}
	e.end()
}

func (e *Engine) nextChunk() (Frame, bool, error) {
	for _, id := range e.order {
		job := e.jobs[id]
		if job == nil || !job.accepted || job.remotePaused || job.waitingWrite || job.finishSent {
			continue
		}
		if e.activeOut != "" && e.activeOut != id {
			continue
		}
		if !e.allowed(job.record) {
			job.accepted = false
			job.remotePaused = true
			job.blockedRelay = true
			e.closeReader(job)
			e.activeOut = ""
			e.changed(false)
			return Frame{}, false, e.emit(control(PauseFrame, job.record.Offer, 0, ReasonRelayApproval))
		}
		e.activeOut = id
		if job.next == job.record.Offer.Chunks() {
			if job.acked != job.next {
				return Frame{}, false, nil
			}
			job.waitingWrite = true
			return creditControl(FinishFrame, job.record.Offer, 0, job.credit), true, nil
		}
		if job.next-job.acked >= UnitChunks {
			return Frame{}, false, nil
		}
		if job.reader == nil {
			var reader io.ReadSeekCloser
			var err error
			readContext, allowed := e.sourceContext(id)
			if !allowed {
				return Frame{}, false, nil
			}
			if source, ok := e.repo.(OutgoingRepository); ok {
				reader, err = source.OpenOutgoing(readContext, id)
			} else {
				reader, err = e.repo.Open(readContext, id)
			}
			if err != nil {
				return Frame{}, false, e.fileFailure(job.record.Offer, err)
			}
			job.reader = reader
		}
		size, err := job.record.Offer.ChunkSize(job.next)
		if err != nil {
			return Frame{}, false, err
		}
		if _, err = job.reader.Seek(int64(job.next)*ChunkBytes, io.SeekStart); err != nil {
			return Frame{}, false, e.fileFailure(job.record.Offer, err)
		}
		data := make([]byte, size)
		if _, err = io.ReadFull(job.reader, data); err != nil {
			return Frame{}, false, e.fileFailure(job.record.Offer, err)
		}
		job.waitingWrite = true
		return Frame{Kind: ChunkFrame, ID: id, SHA256: job.record.Offer.SHA256, Index: job.next, Data: data, Credit: job.credit}, true, nil
	}
	return Frame{}, false, nil
}
func (e *Engine) written(frame Frame) error {
	if frame.Kind == WithdrawFrame {
		if flight := e.withdrawSent[frame.ID]; flight != nil && flight.offer.SHA256 == frame.SHA256 {
			flight.queued = false
			flight.writtenAt = e.now()
		}
	}
	if frame.Kind == CompleteFrame || frame.Kind == DeclineFrame || frame.Kind == WithdrawFrame || frame.Kind == WithdrawAckFrame {
		if offer, ok := e.peerOffers[frame.ID]; ok && offer.SHA256 == frame.SHA256 && e.activeIn != frame.ID {
			delete(e.peerOffers, frame.ID)
			delete(e.peerPaused, frame.ID)
		}
	}
	job := e.jobs[frame.ID]
	if job == nil {
		if frame.Kind == CompleteFrame || frame.Kind == DeclineFrame || frame.Kind == WithdrawAckFrame {
			return e.reload()
		}
		return nil
	}
	if frame.Kind != OfferFrame && (!job.accepted || job.remotePaused) {
		return nil
	}
	if (frame.Kind == ChunkFrame || frame.Kind == FinishFrame) && frame.Credit != job.credit {
		return nil
	}
	switch frame.Kind {
	case OfferFrame:
		job.offerQueued = false
		job.offered = true
	case ChunkFrame:
		if !job.waitingWrite || frame.Index != job.next {
			return ErrProtocol
		}
		job.waitingWrite = false
		job.next++
	case FinishFrame:
		if !job.waitingWrite {
			return ErrProtocol
		}
		job.waitingWrite = false
		job.finishSent = true
		e.closeReader(job)
	}
	return nil
}

func (e *Engine) fileFailure(offer Offer, err error) error {
	e.failed()
	_ = e.repo.Cancel(offer.ID)
	e.drop(offer.ID)
	e.changed(false)
	reason := ReasonUnavailable
	if errors.Is(err, ErrCapacity) {
		reason = ReasonCapacity
	} else if errors.Is(err, ErrIntegrity) {
		reason = ReasonIntegrity
	}
	return e.emit(control(FailureFrame, offer, 0, reason))
}
func (e *Engine) drop(id string) {
	if job := e.jobs[id]; job != nil {
		e.closeReader(job)
		job.accepted = false
		job.remotePaused = true
		job.waitingWrite = false
	}
	if e.activeOut == id {
		e.activeOut = ""
	}
	if e.activeIn == id {
		e.activeIn = ""
		e.expected, e.durable = 0, 0
	}
	kept := e.pending[:0]
	for _, frame := range e.pending {
		if frame.ID != id {
			kept = append(kept, frame)
		}
	}
	e.pending = kept
	select {
	case queued := <-e.output:
		if queued.Epoch == e.epoch && queued.Frame.ID != id {
			e.pending = append([]Frame{queued.Frame}, e.pending...)
		}
	default:
	}
}
