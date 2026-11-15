package transfer

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFullInputQueueWaitsForActorInsteadOfDisconnecting(t *testing.T) {
	e := testEngine(t, newMemoryRepository(), Options{})
	epoch := beginTest(t, e)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- e.request(event{kind: actionEvent, action: func() error { close(started); <-release; return nil }})
	}()
	<-started
	defer close(release)
	o := fixtureOffer(215, []byte("synthetic backpressure"))
	f := control(WithdrawAckFrame, o, 0, ReasonDeclined)
	for range cap(e.events) {
		if err := e.Submit(epoch, f); err != nil {
			t.Fatal(err)
		}
	}
	queued := make(chan error, 1)
	go func() { queued <- e.Submit(epoch, f) }()
	select {
	case err := <-queued:
		t.Fatalf("full bounded actor queue disconnected instead of waiting: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestSessionCancellationInterruptsQueueBackpressureWithoutClosingEngine(t *testing.T) {
	for _, written := range []bool{false, true} {
		t.Run(map[bool]string{false: "incoming", true: "written"}[written], func(t *testing.T) {
			e := testEngine(t, newMemoryRepository(), Options{})
			epoch := beginTest(t, e)
			started, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			go func() {
				_ = e.request(event{kind: actionEvent, action: func() error { close(started); <-release; return nil }})
			}()
			<-started
			o := fixtureOffer(217, []byte("synthetic session queue cancellation"))
			f := control(WithdrawAckFrame, o, 0, ReasonDeclined)
			for range cap(e.events) {
				if err := e.Submit(epoch, f); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				if written {
					done <- e.WrittenContext(ctx, epoch, f)
				} else {
					done <- e.SubmitContext(ctx, epoch, f)
				}
			}()
			select {
			case err := <-done:
				t.Fatal("full queue did not wait", err)
			case <-time.After(20 * time.Millisecond):
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("session cancellation was lost", err)
				}
			case <-time.After(time.Second):
				t.Fatal("session cancellation deadlocked behind local IO")
			}
			if e.ctx.Err() != nil || len(e.events) != cap(e.events) {
				t.Fatal("session wait changed engine lifetime or bounded queue")
			}
		})
	}
}

func TestCloseInterruptsFullQueueAndJoinsActor(t *testing.T) {
	e := testEngine(t, newMemoryRepository(), Options{})
	epoch := beginTest(t, e)
	started := make(chan struct{})
	go func() {
		_ = e.request(event{kind: actionEvent, action: func() error { close(started); <-e.ctx.Done(); return nil }})
	}()
	<-started
	o := fixtureOffer(222, []byte("synthetic full queue shutdown"))
	f := control(WithdrawAckFrame, o, 0, ReasonDeclined)
	for range cap(e.events) {
		if err := e.Submit(epoch, f); err != nil {
			t.Fatal(err)
		}
	}
	queued := make(chan error, 1)
	go func() { queued <- e.Submit(epoch, f) }()
	closed := make(chan struct{})
	go func() { e.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown deadlocked on a full input queue")
	}
	select {
	case err := <-queued:
		if err == nil {
			t.Fatal("shutdown admitted queued work")
		}
	case <-time.After(time.Second):
		t.Fatal("full queue caller survived engine shutdown")
	}
}

func TestPendingWithdrawalsUseReceiptDrivenSmallWindow(t *testing.T) {
	r := newMemoryRepository()
	for i := 1; i <= MaxOffers; i++ {
		o := fixtureOffer(byte(i), []byte("synthetic window"))
		r.records[o.ID] = Record{Offer: o, Direction: Incoming, State: Declined, PendingWithdrawal: true}
	}
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	var first Frame
	for i := 0; i < maxWithdrawalsInFlight; i++ {
		out := nextOutput(t, e)
		if i == 0 {
			first = out.Frame
		}
		if out.Frame.Kind != WithdrawFrame {
			t.Fatal("unexpected replay frame")
		}
	}
	e.Wake()
	barrier(t, e)
	select {
	case <-e.Output():
		t.Fatal("replay exceeded its unacknowledged window")
	case <-time.After(20 * time.Millisecond):
	}
	if err := e.Submit(epoch, Frame{Kind: WithdrawAckFrame, ID: first.ID, SHA256: first.SHA256, Reason: ReasonDeclined}); err != nil {
		t.Fatal(err)
	}
	if out := nextOutput(t, e); out.Frame.Kind != WithdrawFrame || out.Frame.ID == first.ID {
		t.Fatal("receipt did not admit exactly one new withdrawal")
	}
	barrier(t, e)
	select {
	case <-e.Output():
		t.Fatal("one receipt admitted multiple withdrawals")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestClearHistoryPreservesQueuedCancellationReceipt(t *testing.T) {
	e := testEngine(t, newMemoryRepository(), Options{})
	epoch := beginTest(t, e)
	o := fixtureOffer(216, []byte("synthetic terminal receipt"))
	if err := e.Submit(epoch, control(WithdrawFrame, o, 0, 0)); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if err := e.ClearHistory(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-e.Output():
		if out.Frame.Kind != WithdrawAckFrame || out.Frame.ID != o.ID {
			t.Fatal("wrong control survived history clear")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("history clear discarded an authenticated cancellation receipt")
	}
}

func TestHistoryClearKeepsBothOutputAndPendingTerminalObligations(t *testing.T) {
	for _, fail := range []bool{true, false} {
		t.Run(map[bool]string{true: "failed clear", false: "successful clear"}[fail], func(t *testing.T) {
			r := newMemoryRepository()
			e := testEngine(t, r, Options{})
			epoch := beginTest(t, e)
			completed := fixtureOffer(218, []byte("synthetic completed file"))
			unknown := fixtureOffer(219, []byte("synthetic queued receipt"))
			r.records[completed.ID] = Record{Offer: completed, Direction: Incoming, State: Complete, NextChunk: completed.Chunks()}
			if err := e.request(event{kind: actionEvent, action: func() error {
				e.peerOffers[completed.ID] = completed
				e.output <- Outbound{Epoch: epoch, Frame: control(WithdrawAckFrame, unknown, 0, ReasonDeclined)}
				return e.emit(control(CompleteFrame, completed, 0, 0))
			}}); err != nil {
				t.Fatal(err)
			}
			synthetic := errors.New("synthetic rejected history clear")
			err := e.ClearHistory(func() error {
				if fail {
					return synthetic
				}
				r.mu.Lock()
				v := r.records[completed.ID]
				v.Hidden = true
				r.records[completed.ID] = v
				r.mu.Unlock()
				return nil
			})
			if fail && !errors.Is(err, synthetic) || !fail && err != nil {
				t.Fatal("clear result changed", err)
			}
			seen := map[Kind]bool{}
			for range 2 {
				out := nextOutput(t, e)
				seen[out.Frame.Kind] = true
				if err := e.Written(epoch, out.Frame); err != nil {
					t.Fatal(err)
				}
			}
			barrier(t, e)
			if !seen[WithdrawAckFrame] || !seen[CompleteFrame] {
				t.Fatal("history clear discarded a peer's terminal obligation")
			}
			select {
			case out := <-e.Output():
				t.Fatal("clear duplicated terminal obligation", out.Frame.Kind)
			case <-time.After(10 * time.Millisecond):
			}
		})
	}
}
