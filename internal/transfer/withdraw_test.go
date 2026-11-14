package transfer

import (
	"bytes"
	"testing"
)

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
			value.Hidden, value.State = true, Declined
			r.records[id] = value
		}
		return nil
	}); err != nil {
		t.Fatal("clearing the supported offer set overflowed controls", err)
	}
	seen := make(map[string]bool)
	for range MaxOffers {
		frame := nextOutput(t, e).Frame
		if frame.Kind != DeclineFrame || seen[frame.ID] {
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
