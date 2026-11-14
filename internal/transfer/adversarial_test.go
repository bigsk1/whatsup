package transfer

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func expectFailure(t *testing.T, e *Engine) {
	t.Helper()
	select {
	case f := <-e.Failures():
		if !errors.Is(f.Err, ErrProtocol) {
			t.Fatalf("failure=%v", f.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("invalid attachment did not terminate session")
	}
}

func TestUnapprovedOutOfOrderAndSwappedChunksAreRejected(t *testing.T) {
	for _, mode := range []string{"unapproved", "out-of-order", "swapped-hash", "wrong-length"} {
		t.Run(mode, func(t *testing.T) {
			r := newMemoryRepository()
			e := testEngine(t, r, Options{})
			epoch := beginTest(t, e)
			o := fixtureOffer(21, bytes.Repeat([]byte{19}, ChunkBytes*2))
			if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}); err != nil {
				t.Fatal(err)
			}
			barrier(t, e)
			credit := [16]byte{1}
			if mode != "unapproved" {
				if err := e.Accept(o.ID); err != nil {
					t.Fatal(err)
				}
				out := nextOutput(t, e)
				credit = out.Frame.Credit
				if out.Frame.Kind != AcceptFrame {
					t.Fatal("missing receiver approval")
				}
				if err := e.Written(epoch, out.Frame); err != nil {
					t.Fatal(err)
				}
			}
			f := Frame{Kind: ChunkFrame, ID: o.ID, SHA256: o.SHA256, Data: bytes.Repeat([]byte{19}, ChunkBytes), Credit: credit}
			switch mode {
			case "out-of-order":
				f.Index = 1
			case "swapped-hash":
				f.SHA256[0] ^= 1
			case "wrong-length":
				f.Data = f.Data[:1]
			}
			if err := e.Submit(epoch, f); err != nil {
				t.Fatal(err)
			}
			expectFailure(t, e)
			r.mu.Lock()
			calls := r.appendCalls
			r.mu.Unlock()
			if calls != 0 {
				t.Fatal("invalid chunk reached repository")
			}
		})
	}
}

func TestCapacityFailureAndDeclineRaceKeepProtocolUsable(t *testing.T) {
	r := newMemoryRepository()
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	data := bytes.Repeat([]byte{27}, ChunkBytes)
	o := fixtureOffer(22, data)
	if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if err := e.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	credit := nextOutput(t, e).Frame.Credit
	r.mu.Lock()
	r.capacity = true
	r.mu.Unlock()
	chunk := Frame{Kind: ChunkFrame, ID: o.ID, SHA256: o.SHA256, Data: data, Credit: credit}
	if err := e.Submit(epoch, chunk); err != nil {
		t.Fatal(err)
	}
	out := nextOutput(t, e)
	if out.Frame.Kind != FailureFrame || out.Frame.Reason != ReasonCapacity {
		t.Fatal("full disk was not an attachment-scoped refusal")
	}
	if err := e.Decline(o.ID); err != nil {
		t.Fatal(err)
	}
	_ = nextOutput(t, e)
	// The sender may already have written a bounded chunk before seeing the
	// decline. It is discarded and answered, without killing the chat session.
	if err := e.Submit(epoch, chunk); err != nil {
		t.Fatal(err)
	}
	out = nextOutput(t, e)
	if out.Frame.Kind != WithdrawFrame {
		t.Fatal("in-flight declined chunk was not safely drained")
	}
	select {
	case <-e.Failures():
		t.Fatal("declining a file disconnected messaging")
	default:
	}
}

func TestFinalVerificationDoesNotBlockOffersAndCancelJoinsIt(t *testing.T) {
	r := newMemoryRepository()
	gate := make(chan struct{})
	r.finalGate = gate
	r.finalStarted = make(chan struct{}, 1)
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	data := []byte("synthetic final verification")
	o := fixtureOffer(23, data)
	if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if err := e.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	credit := nextOutput(t, e).Frame.Credit
	if err := e.Submit(epoch, Frame{Kind: ChunkFrame, ID: o.ID, SHA256: o.SHA256, Data: data, Credit: credit}); err != nil {
		t.Fatal(err)
	}
	if out := nextOutput(t, e); out.Frame.Kind != CheckpointFrame {
		t.Fatal("missing durable final checkpoint")
	}
	if err := e.Submit(epoch, creditControl(FinishFrame, o, 0, credit)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.finalStarted:
	case <-time.After(time.Second):
		t.Fatal("verification did not start")
	}
	other := fixtureOffer(24, []byte("other synthetic"))
	if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: other.ID, SHA256: other.SHA256, Offer: other}); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if _, err := r.Get(other.ID); err != nil {
		t.Fatal("final hash blocked control processing")
	}
	if err := e.Cancel(o.ID); err != nil {
		t.Fatal(err)
	}
	out := nextOutput(t, e)
	if out.Frame.Kind != PauseFrame {
		t.Fatal("cancel did not stop verification")
	}
	v, _ := r.Get(o.ID)
	if v.State != Paused {
		t.Fatal("cancelled unverified file became visible")
	}
}

func TestPauseSurvivesReofferUntilExplicitResume(t *testing.T) {
	r := newMemoryRepository()
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	o := fixtureOffer(25, []byte("synthetic"))
	offer := Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}
	if err := e.Submit(epoch, offer); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if err := e.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	_ = nextOutput(t, e)
	if err := e.Cancel(o.ID); err != nil {
		t.Fatal(err)
	}
	_ = nextOutput(t, e)
	if err := e.Submit(epoch, offer); err != nil {
		t.Fatal(err)
	}
	if out := nextOutput(t, e); out.Frame.Kind != PauseFrame {
		t.Fatal("reoffer bypassed receiver local pause")
	}
	if err := e.Resume(o.ID); err != nil {
		t.Fatal(err)
	}
	if out := nextOutput(t, e); out.Frame.Kind != AcceptFrame {
		t.Fatal("explicit resume did not grant credit")
	}
}

func TestEndCancelsVerificationWithoutWaitingForWholeFile(t *testing.T) {
	r := newMemoryRepository()
	r.finalGate = make(chan struct{})
	r.finalStarted = make(chan struct{}, 1)
	e := testEngine(t, r, Options{})
	epoch := beginTest(t, e)
	o := fixtureOffer(26, nil)
	if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	if err := e.Accept(o.ID); err != nil {
		t.Fatal(err)
	}
	credit := nextOutput(t, e).Frame.Credit
	if err := e.Submit(epoch, creditControl(FinishFrame, o, 0, credit)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.finalStarted:
	case <-time.After(time.Second):
		t.Fatal("verification did not start")
	}
	done := make(chan error, 1)
	go func() { done <- e.End(epoch) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("session close waited for whole-file verification")
	}
	if err := e.Submit(epoch, Frame{Kind: OfferFrame, ID: o.ID, SHA256: o.SHA256, Offer: o}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRapidReceiverCancelResumeRejectsAlreadyDequeuedOldCredit(t *testing.T) {
	data := bytes.Repeat([]byte{51}, ChunkBytes*18+29)
	o := fixtureOffer(27, data)
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
	deliver(t, b, a, ae, nextOutput(t, b))
	old := nextOutput(t, a) // Already held by TLS writer when Cancel arrives.
	if old.Frame.Kind != ChunkFrame {
		t.Fatal("missing old chunk")
	}
	if err := b.Cancel(o.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Resume(o.ID); err != nil {
		t.Fatal(err)
	}
	accept := nextOutput(t, b)
	if accept.Frame.Kind != AcceptFrame || accept.Frame.Credit == old.Frame.Credit {
		t.Fatal("resume reused receive credit")
	}
	deliver(t, b, a, ae, accept)
	barrier(t, a)
	deliver(t, a, b, be, old)
	barrier(t, b)
	br.mu.Lock()
	calls := br.appendCalls
	br.mu.Unlock()
	if calls != 0 {
		t.Fatal("old credit wrote into resumed transfer")
	}
	pumpUntil(t, a, b, ae, be, func() bool { v, _ := ar.Get(o.ID); return v.State == Complete }, nil)
}

func TestRapidSenderCancelResumeReoffersAndReplacesOldWindow(t *testing.T) {
	data := bytes.Repeat([]byte{62}, ChunkBytes*18+31)
	o := fixtureOffer(28, data)
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
	deliver(t, b, a, ae, nextOutput(t, b))
	old := nextOutput(t, a)
	if err := a.Cancel(o.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.Resume(o.ID); err != nil {
		t.Fatal(err)
	}
	// The old chunk may still finish writing before the new offer. The new
	// offer must issue fresh credit and discard its incomplete buffered unit.
	deliver(t, a, b, be, old)
	pumpUntil(t, a, b, ae, be, func() bool { v, _ := ar.Get(o.ID); return v.State == Complete }, nil)
}
