package transfer

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestOfflineWithdrawalIsReplayedAfterEngineRecreation(t *testing.T) {
	data := []byte("synthetic durable cancellation")
	o := fixtureOffer(199, data)
	ar, br := newMemoryRepository(), newMemoryRepository()
	ar.stage(o, data)
	a, b := testEngine(t, ar, Options{}), testEngine(t, br, Options{})
	beginTest(t, a)
	be := beginTest(t, b)
	if err := a.Queue(o.ID, false); err != nil {
		t.Fatal(err)
	}
	deliver(t, a, b, be, nextOutput(t, a))
	barrier(t, b)
	if err := b.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.End(a.epoch); err != nil {
		t.Fatal(err)
	}
	if err := b.End(be); err != nil {
		t.Fatal(err)
	}
	if err := a.Withdraw(o.ID); err != nil {
		t.Fatal(err)
	}
	a.Close()
	a = testEngine(t, ar, Options{})
	beginTest(t, a)
	be, _ = b.Begin(context.Background(), func() error { return nil })
	select {
	case f := <-a.Output():
		deliver(t, a, b, be, f)
		barrier(t, b)
		if r, _ := br.Get(o.ID); r.State != Declined {
			t.Fatal("remote slot survived an offline withdrawal")
		}
		// Drop the first receipt, then reconnect again. The sender must repeat
		// its durable intent and the already-terminal receiver must acknowledge.
		_ = nextOutput(t, b)
		_ = a.End(a.epoch)
		_ = b.End(be)
		ae := beginTest(t, a)
		be = beginTest(t, b)
		deliver(t, a, b, be, nextOutput(t, a))
		deliver(t, b, a, ae, nextOutput(t, b))
		barrier(t, a)
		if r, _ := ar.Get(o.ID); r.PendingWithdrawal {
			t.Fatal("authenticated terminal receipt did not retire intent")
		}
		_ = a.End(ae)
		beginTest(t, a)
		select {
		case <-a.Output():
			t.Fatal("retired cancellation was repeated")
		case <-time.After(20 * time.Millisecond):
		}
	case <-time.After(time.Second):
		t.Fatal("offline withdrawal was lost across engine recreation")
	}
}

func TestSequentialWithdrawalsRetireSessionOffersBeyondAdmissionBound(t *testing.T) {
	for _, local := range []bool{true, false} {
		t.Run(map[bool]string{true: "local withdrawal", false: "peer withdrawal"}[local], func(t *testing.T) {
			r := newMemoryRepository()
			base := time.Now()
			var elapsed atomic.Int64
			e := testEngine(t, r, Options{Now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }})
			epoch := beginTest(t, e)
			const total = MaxOffers + MaxActive + 8
			for i := 1; i <= total; i++ {
				// Each iteration redeems two offer-rate tokens, including the
				// replay. Advance the injectable monotonic clock only after the
				// normal 32-token/30-second window, keeping the production bound.
				if i > 1 && (i-1)%16 == 0 {
					elapsed.Add(int64(30 * time.Second))
				}
				body := []byte("synthetic sequential withdrawal")
				o := fixtureOffer(byte(i), body)
				offer := Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}
				if err := e.Submit(epoch, offer); err != nil {
					t.Fatal(err)
				}
				barrier(t, e)
				if err := e.Accept(o.ID); err != nil {
					t.Fatal("terminal offers exhausted receiver admission", i, err)
				}
				credit := nextOutput(t, e).Frame.Credit
				if local {
					if err := e.Withdraw(o.ID); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := e.Submit(epoch, control(WithdrawFrame, o, 0, 0)); err != nil {
						t.Fatal(err)
					}
				}
				terminal := nextOutput(t, e)
				if err := e.Written(epoch, terminal.Frame); err != nil {
					t.Fatal(err)
				}
				if local {
					if err := e.Submit(epoch, control(WithdrawAckFrame, o, 0, ReasonDeclined)); err != nil {
						t.Fatal(err)
					}
				}
				barrier(t, e)
				// Written terminal controls release only in-memory session slots.
				// The durable tombstone must still reject old credit and replay.
				if err := e.Submit(epoch, Frame{Kind: ChunkFrame, ID: o.ID, SHA256: o.SHA256, Credit: credit, Data: body}); err != nil {
					t.Fatal(err)
				}
				stale := nextOutput(t, e)
				if err := e.Written(epoch, stale.Frame); err != nil {
					t.Fatal(err)
				}
				if err := e.Submit(epoch, offer); err != nil {
					t.Fatal(err)
				}
				replayed := nextOutput(t, e)
				if replayed.Frame.Kind != DeclineFrame {
					t.Fatal("cancelled offer replay regained approval")
				}
				if err := e.Written(epoch, replayed.Frame); err != nil {
					t.Fatal(err)
				}
				barrier(t, e)
				if record, _ := r.Get(o.ID); record.State != Declined || record.PendingWithdrawal {
					t.Fatal("terminal replay revived work or receipt obligation")
				}
			}
			select {
			case failure := <-e.Failures():
				t.Fatal("sequential terminal controls disconnected chat", failure.Err)
			default:
			}
		})
	}
}

func TestWithdrawUnknownIsIdempotentAndDoesNotCreateHistoryOrAckLoops(t *testing.T) {
	r := newMemoryRepository()
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	o := fixtureOffer(198, []byte("synthetic unknown cancellation"))
	for range 3 {
		if err := e.Submit(epoch, control(WithdrawFrame, o, 0, 0)); err != nil {
			t.Fatal(err)
		}
		ack := nextOutput(t, e).Frame
		if ack.Kind != WithdrawAckFrame || ack.Reason != ReasonDeclined {
			t.Fatal("unknown cancellation did not receive a terminal receipt")
		}
		if err := e.Submit(epoch, ack); err != nil {
			t.Fatal(err)
		}
		barrier(t, e)
	}
	if records, _ := r.List(); len(records) != 0 {
		t.Fatal("unknown cancellation consumed quota/history")
	}
	select {
	case <-e.Output():
		t.Fatal("receipt generated a receipt loop")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestSimultaneousWithdrawalsRetireIntentEvenWhenOwnRequestWasNotWritten(t *testing.T) {
	r := newMemoryRepository()
	o := fixtureOffer(194, []byte("synthetic simultaneous cancellation"))
	r.records[o.ID] = Record{Offer: o, Direction: Outgoing, State: Declined, PendingWithdrawal: true}
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	if err := e.Submit(epoch, control(WithdrawFrame, o, 0, 0)); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if record, _ := r.Get(o.ID); record.PendingWithdrawal {
		t.Fatal("simultaneous remote intent did not settle local cancellation")
	}
}

func TestWithdrawalAfterReconnectNeedsOnlyDurableIDAndHash(t *testing.T) {
	for _, direction := range []string{Incoming, Outgoing} {
		t.Run(direction, func(t *testing.T) {
			r := newMemoryRepository()
			o := fixtureOffer(197, []byte("synthetic retained acceptance"))
			state := Accepted
			if direction == Outgoing {
				state = Paused
			}
			r.records[o.ID] = Record{Offer: o, Direction: direction, State: state}
			e := testEngine(t, r, Options{})
			epoch := beginTest(t, e)
			if err := e.Submit(epoch, control(WithdrawFrame, o, 0, 0)); err != nil {
				t.Fatal(err)
			}
			ack := nextOutput(t, e).Frame
			if ack.Kind != WithdrawAckFrame {
				t.Fatal("durable acceptance did not acknowledge cancellation")
			}
			if current, _ := r.Get(o.ID); current.State != Declined {
				t.Fatal("reconnected transfer retained its active slot")
			}
		})
	}
}

func TestCompletedPeerWinsWithdrawalRaceWithoutClaimingRemoteDeletion(t *testing.T) {
	o := fixtureOffer(196, []byte("synthetic completed remote file"))
	ar, br := newMemoryRepository(), newMemoryRepository()
	ar.records[o.ID] = Record{Offer: o, Direction: Outgoing, State: Queued}
	br.records[o.ID] = Record{Offer: o, Direction: Incoming, State: Complete, NextChunk: o.Chunks()}
	a, b := testEngine(t, ar, Options{}), testEngine(t, br, Options{})
	if err := a.Withdraw(o.ID); err != nil {
		t.Fatal(err)
	}
	ae, be := beginTest(t, a), beginTest(t, b)
	deliver(t, a, b, be, nextOutput(t, a))
	ack := nextOutput(t, b)
	if ack.Frame.Reason != ReasonCompleted {
		t.Fatal("completed peer did not report its authoritative result")
	}
	deliver(t, b, a, ae, ack)
	barrier(t, a)
	if current, _ := ar.Get(o.ID); current.State != Declined || current.PendingWithdrawal || !current.PeerCompleted || WithdrawalWarning(current) != "peer-completed" {
		t.Fatal("local cancellation misrepresented the completed peer copy")
	}
	if current, _ := br.Get(o.ID); current.State != Complete {
		t.Fatal("cancel deleted a completed peer file")
	}
	if err := a.Withdraw(o.ID); err != nil {
		t.Fatal(err)
	}
	if current, _ := ar.Get(o.ID); !current.PeerCompleted {
		t.Fatal("repeated cancel erased terminal outcome")
	}
}

func TestFailedClearRestoresReceiverAndIgnoresOldCredit(t *testing.T) {
	r := newMemoryRepository()
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	body := []byte("synthetic receiver after failed clear")
	o := fixtureOffer(195, body)
	if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if err := e.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	oldCredit := nextOutput(t, e).Frame.Credit
	synthetic := errors.New("synthetic clear transaction interruption")
	if err := e.ClearHistory(func() error { return synthetic }); !errors.Is(err, synthetic) {
		t.Fatal("clear failure was lost")
	}
	newCredit := nextOutput(t, e).Frame.Credit
	if newCredit == oldCredit || newCredit == [16]byte{} {
		t.Fatal("failed clear did not restore receiver with fresh credit")
	}
	for _, credit := range [][16]byte{oldCredit, newCredit} {
		if err := e.Submit(epoch, Frame{Kind: ChunkFrame, ID: o.ID, SHA256: o.SHA256, Data: body, Credit: credit}); err != nil {
			t.Fatal(err)
		}
		barrier(t, e)
	}
	if f := nextOutput(t, e).Frame; f.Kind != CheckpointFrame || f.NextChunk != 1 {
		t.Fatal("receiver lost durable state after failed clear")
	}
	select {
	case f := <-e.Failures():
		t.Fatal("failed clear poisoned chat", f.Err)
	default:
	}
}

func TestWithdrawIsTerminalOnBothSidesAndStaleFramesDoNotReviveIt(t *testing.T) {
	for _, actor := range []string{"sender", "receiver"} {
		t.Run(actor, func(t *testing.T) {
			data := bytes.Repeat([]byte{73}, ChunkBytes*3)
			o := fixtureOffer(87, data)
			ar, br := newMemoryRepository(), newMemoryRepository()
			ar.stage(o, data)
			a, b := testEngine(t, ar, Options{}), testEngine(t, br, Options{})
			ae, be := beginTest(t, a), beginTest(t, b)
			if err := a.Queue(o.ID, false); err != nil {
				t.Fatal(err)
			}
			deliver(t, a, b, be, nextOutput(t, a))
			barrier(t, b)
			if err := b.Accept(o.ID); err != nil {
				t.Fatal(err)
			}
			accept := nextOutput(t, b)
			deliver(t, b, a, ae, accept)
			staleChunk := nextOutput(t, a)
			if staleChunk.Frame.Kind != ChunkFrame {
				t.Fatal("missing in-flight chunk")
			}
			if actor == "sender" {
				if err := a.Withdraw(o.ID); err != nil {
					t.Fatal(err)
				}
				deliver(t, a, b, be, nextOutput(t, a))
			} else {
				if err := b.Withdraw(o.ID); err != nil {
					t.Fatal(err)
				}
				deliver(t, b, a, ae, nextOutput(t, b))
			}
			barrier(t, a)
			barrier(t, b)
			// A frame already taken by the TLS writer and an old receiver grant
			// remain inert after authenticated cancellation.
			if err := b.Submit(be, staleChunk.Frame); err != nil {
				t.Fatal(err)
			}
			if err := a.Submit(ae, accept.Frame); err != nil {
				t.Fatal(err)
			}
			barrier(t, a)
			barrier(t, b)
			for _, item := range []struct {
				e *Engine
				r *memoryRepository
			}{{a, ar}, {b, br}} {
				v, err := item.r.Get(o.ID)
				if err != nil || v.State != Declined {
					t.Fatal("withdraw did not become terminal", err)
				}
				if err := item.e.Resume(o.ID); err == nil {
					t.Fatal("withdraw resumed")
				}
				select {
				case f := <-item.e.Failures():
					t.Fatal("withdraw disconnected the shared stream", f.Err)
				default:
				}
			}
		})
	}
}

func TestClearMaximumOfferSetKeepsTerminalControlsBoundedAndStreamLive(t *testing.T) {
	r := newMemoryRepository()
	for i := 1; i <= MaxOffers; i++ {
		o := fixtureOffer(byte(i), []byte("synthetic pending offer"))
		if err := r.Offer(o); err != nil {
			t.Fatal(err)
		}
	}
	e := testEngine(t, r, Options{})
	beginTest(t, e)
	if err := e.ClearHistory(func() error {
		r.mu.Lock()
		defer r.mu.Unlock()
		for id, value := range r.records {
			value.Hidden, value.State, value.PendingWithdrawal = true, Declined, true
			r.records[id] = value
		}
		return nil
	}); err != nil {
		t.Fatal("clearing the supported offer set overflowed controls", err)
	}
	seen := make(map[string]bool)
	for range MaxOffers {
		frame := nextOutput(t, e).Frame
		if frame.Kind != WithdrawFrame || seen[frame.ID] {
			t.Fatal("missing or repeated terminal response")
		}
		seen[frame.ID] = true
	}
	barrier(t, e)
	select {
	case failed := <-e.Failures():
		t.Fatal("history clear disconnected messaging", failed.Err)
	default:
	}
}
