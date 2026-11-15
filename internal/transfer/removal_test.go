package transfer

import (
	"context"
	"errors"
	"testing"
	"time"
)

type removalMemory struct {
	*memoryRepository
	removed      chan struct{}
	cleanupError error
}

func (r removalMemory) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.records[id]
	if !ok {
		return ErrUnavailable
	}
	v.Hidden = true
	if v.State != Complete {
		if v.State != Declined && v.State != Draft {
			v.PendingWithdrawal = true
		}
		v.State = Declined
		v.NextChunk = 0
	}
	r.records[id] = v
	delete(r.data, id)
	if r.removed != nil {
		select {
		case r.removed <- struct{}{}:
		default:
		}
	}
	return r.cleanupError
}

func TestRemoveCleanupFailureStillRetiresCommittedHiddenSend(t *testing.T) {
	r := &removalMemory{memoryRepository: newMemoryRepository(), cleanupError: errors.New("synthetic post-commit object cleanup failure")}
	o := fixtureOffer(189, []byte("synthetic already cached offer"))
	r.stage(o, []byte("synthetic already cached offer"))
	e := testEngine(t, r, Options{})
	beginTest(t, e)
	if err := e.Queue(o.ID, false); err != nil {
		t.Fatal(err)
	}
	held := nextOutput(t, e)
	if err := e.Remove(o.ID); !errors.Is(err, r.cleanupError) {
		t.Fatal("cleanup result was hidden", err)
	}
	if sent, err := held.WriteIfCurrent(func() error { t.Fatal("committed hidden item was still offered"); return nil }); sent || err != nil {
		t.Fatal("cleanup failure retained outgoing authority")
	}
	if out := nextOutput(t, e); out.Frame.Kind != WithdrawFrame {
		t.Fatal("cleanup failure lost durable withdrawal")
	}
}

func TestRemoveRetiresFrameAlreadyHeldByWriterAndKeepsWithdrawal(t *testing.T) {
	r := removalMemory{memoryRepository: newMemoryRepository()}
	o := fixtureOffer(190, []byte("synthetic cached outgoing chunk"))
	r.stage(o, []byte("synthetic cached outgoing chunk"))
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	if err := e.Queue(o.ID, false); err != nil {
		t.Fatal(err)
	}
	offered := nextOutput(t, e)
	if err := e.Written(epoch, offered.Frame); err != nil {
		t.Fatal(err)
	}
	if err := e.Submit(epoch, creditControl(AcceptFrame, o, 0, [16]byte{1})); err != nil {
		t.Fatal(err)
	}
	held := nextOutput(t, e)
	if held.Frame.Kind != ChunkFrame {
		t.Fatal("expected cached file bytes")
	}
	if err := e.Remove(o.ID); err != nil {
		t.Fatal(err)
	}
	called := false
	if written, err := held.WriteIfCurrent(func() error { called = true; return nil }); err != nil || written || called {
		t.Fatal("queued file bytes survived successful delete", err)
	}
	terminal := nextOutput(t, e)
	if terminal.Frame.Kind != WithdrawFrame || terminal.Frame.ID != o.ID {
		t.Fatal("deleted send lost its withdrawal")
	}
	if written, err := terminal.WriteIfCurrent(func() error { return nil }); err != nil || !written {
		t.Fatal("delete suppressed terminal control", err)
	}
	if err := e.Submit(epoch, control(WithdrawAckFrame, o, 0, ReasonDeclined)); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if current, _ := r.Get(o.ID); !current.Hidden || current.PendingWithdrawal || current.State != Declined {
		t.Fatal("receipt revived deleted local data")
	}
	if err := e.Remove(o.ID); err != nil {
		t.Fatal("delete is not idempotent", err)
	}
}

func TestRemoveWaitsForBoundedInflightWriteThenRetiresItsPermit(t *testing.T) {
	r := removalMemory{memoryRepository: newMemoryRepository(), removed: make(chan struct{}, 1)}
	o := fixtureOffer(191, []byte("synthetic current wire frame"))
	r.stage(o, []byte("synthetic current wire frame"))
	e := testEngine(t, r, Options{})
	beginTest(t, e)
	if err := e.Queue(o.ID, false); err != nil {
		t.Fatal(err)
	}
	held := nextOutput(t, e)
	entered, release, written := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(written)
		_, _ = held.WriteIfCurrent(func() error { close(entered); <-release; return nil })
	}()
	<-entered
	done := make(chan error, 1)
	go func() { done <- e.Remove(o.ID) }()
	select {
	case <-r.removed:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("deletion did not reach durable removal")
	}
	select {
	case err := <-done:
		close(release)
		t.Fatal("delete completed before in-flight write retired", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-written
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("delete did not join bounded write")
	}
	if sent, err := held.WriteIfCurrent(func() error { t.Fatal("retired write repeated"); return nil }); sent || err != nil {
		t.Fatal("retired frame still writable")
	}
}

func TestRemoveCompletedIncomingPreservesReceiptAndAnotherSend(t *testing.T) {
	r := removalMemory{memoryRepository: newMemoryRepository()}
	completed := fixtureOffer(192, []byte("synthetic completed incoming item"))
	other := fixtureOffer(193, []byte("synthetic unrelated queued item"))
	r.records[completed.ID] = Record{Offer: completed, Direction: Incoming, State: Complete, NextChunk: completed.Chunks()}
	r.stage(other, []byte("synthetic unrelated queued item"))
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	if err := e.request(event{kind: actionEvent, action: func() error {
		e.peerOffers[completed.ID] = completed
		return e.emit(control(CompleteFrame, completed, 0, 0))
	}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Remove(completed.ID); err != nil {
		t.Fatal(err)
	}
	if out := nextOutput(t, e); out.Frame.Kind != CompleteFrame || out.Frame.ID != completed.ID {
		t.Fatal("delete lost queued completion receipt")
	}
	if err := e.Queue(other.ID, false); err != nil {
		t.Fatal(err)
	}
	out := nextOutput(t, e)
	if out.Frame.Kind != OfferFrame || out.Frame.ID != other.ID {
		t.Fatal("single delete altered another send")
	}
	if sent, err := out.WriteIfCurrent(func() error { return nil }); !sent || err != nil {
		t.Fatal("another sender's write permit was retired")
	}
	if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: completed.ID, SHA256: completed.SHA256, Offer: completed}); err != nil {
		t.Fatal(err)
	}
	if out := nextOutput(t, e); out.Frame.Kind != CompleteFrame {
		t.Fatal("completed replay lost idempotent receipt")
	}
	if current, _ := r.Get(completed.ID); !current.Hidden || current.State != Complete {
		t.Fatal("replay resurrected deleted card")
	}
}

type removalFinalRace struct {
	removalMemory
	started chan struct{}
}

func (r removalFinalRace) Finalize(ctx context.Context, id string) error {
	close(r.started)
	<-ctx.Done()
	r.mu.Lock()
	v := r.records[id]
	v.State = Complete
	v.NextChunk = v.Offer.Chunks()
	r.records[id] = v
	r.mu.Unlock()
	return nil // A durable completion won the cancellation race.
}

func TestRemoveJoinsFinalizeAndRetainsCompletionThatWonTheRace(t *testing.T) {
	o := fixtureOffer(194, nil)
	r := removalFinalRace{removalMemory: removalMemory{memoryRepository: newMemoryRepository()}, started: make(chan struct{})}
	r.records[o.ID] = Record{Offer: o, Direction: Incoming, State: Accepted}
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	if err := e.request(event{kind: actionEvent, action: func() error { e.peerOffers[o.ID] = o; return e.promoteReceiver() }}); err != nil {
		t.Fatal(err)
	}
	accepted := nextOutput(t, e)
	if err := e.Submit(epoch, creditControl(FinishFrame, o, 0, accepted.Frame.Credit)); err != nil {
		t.Fatal(err)
	}
	<-r.started
	if err := e.Remove(o.ID); err != nil {
		t.Fatal(err)
	}
	if current, _ := r.Get(o.ID); !current.Hidden || current.State != Complete || current.PendingWithdrawal {
		t.Fatal("delete falsely canceled a durable completion")
	}
	if out := nextOutput(t, e); out.Frame.Kind != CompleteFrame {
		t.Fatal("completion race stranded peer sender")
	}
	if err := e.Remove("../invalid"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("invalid deletion target accepted")
	}
}

func TestRemoveUsesOnlyLegacyTerminalGrammarForOlderPeers(t *testing.T) {
	for _, incoming := range []bool{true, false} {
		t.Run(map[bool]string{true: "incoming", false: "outgoing"}[incoming], func(t *testing.T) {
			r := removalMemory{memoryRepository: newMemoryRepository()}
			o := fixtureOffer(195, []byte("synthetic legacy deletion"))
			if incoming {
				r.records[o.ID] = Record{Offer: o, Direction: Incoming, State: Offered}
			} else {
				r.stage(o, []byte("synthetic legacy deletion"))
				if err := r.QueueOutgoing(o.ID, false); err != nil {
					t.Fatal(err)
				}
			}
			e := testEngine(t, r, Options{})
			epoch, err := e.BeginWithWithdrawals(t.Context(), func() error { return nil }, false)
			if err != nil {
				t.Fatal(err)
			}
			var held Outbound
			if incoming {
				if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}); err != nil {
					t.Fatal(err)
				}
				barrier(t, e)
			} else {
				held = nextOutput(t, e)
			}
			if err := e.Remove(o.ID); err != nil {
				t.Fatal(err)
			}
			if incoming {
				if out := nextOutput(t, e); out.Frame.Kind != DeclineFrame || out.Frame.Reason != ReasonDeclined {
					t.Fatal("legacy sender did not get its supported terminal response")
				}
			} else if sent, err := held.WriteIfCurrent(func() error { t.Fatal("legacy queued file was sent after delete"); return nil }); sent || err != nil {
				t.Fatal("legacy pending frame retained authority")
			}
			barrier(t, e)
			select {
			case out := <-e.Output():
				t.Fatal("delete invented unsupported legacy wire grammar", out.Frame.Kind)
			case <-time.After(10 * time.Millisecond):
			}
		})
	}
}
