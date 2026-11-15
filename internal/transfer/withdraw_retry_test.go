package transfer

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestLostWithdrawalReceiptRetriesWithoutReconnecting(t *testing.T) {
	r := newMemoryRepository()
	o := fixtureOffer(231, []byte("synthetic live cancellation retry"))
	r.records[o.ID] = Record{Offer: o, Direction: Outgoing, State: Declined, PendingWithdrawal: true}
	base := time.Now()
	var elapsed atomic.Int64
	e := testEngine(t, r, Options{Now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }})
	epoch := beginTest(t, e)
	first := nextOutput(t, e)
	if err := e.Written(epoch, first.Frame); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	elapsed.Store(int64(31 * time.Second))
	e.Wake()
	select {
	case retry := <-e.Output():
		if retry.Epoch != epoch || retry.Frame.Kind != first.Frame.Kind || retry.Frame.ID != first.Frame.ID || retry.Frame.SHA256 != first.Frame.SHA256 {
			t.Fatal("lost receipt changed its session or cancellation identity")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("lost receipt stranded its cancellation until reconnect")
	}
}

func TestWithdrawalRetryRequiresWriteAndKeepsBoundedBackoffUntilReceipt(t *testing.T) {
	r := newMemoryRepository()
	o := fixtureOffer(234, []byte("synthetic bounded retry schedule"))
	r.records[o.ID] = Record{Offer: o, Direction: Outgoing, State: Declined, PendingWithdrawal: true}
	base := time.Now()
	var elapsed atomic.Int64
	e := testEngine(t, r, Options{Now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }})
	epoch := beginTest(t, e)
	frame := nextOutput(t, e).Frame
	quiet := func() {
		t.Helper()
		e.Wake()
		barrier(t, e)
		select {
		case <-e.Output():
			t.Fatal("withdrawal was retried before its paced write deadline")
		case <-time.After(10 * time.Millisecond):
		}
	}
	elapsed.Store(int64(time.Hour))
	quiet() // Merely queued, not yet confirmed as written.
	if err := e.Written(epoch, frame); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	for _, delay := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second} {
		elapsed.Add(int64(delay - time.Nanosecond))
		quiet()
		elapsed.Add(int64(time.Nanosecond))
		e.Wake()
		retry := nextOutput(t, e)
		if retry.Epoch != epoch || retry.Frame.ID != o.ID || retry.Frame.SHA256 != o.SHA256 || retry.Frame.Kind != WithdrawFrame {
			t.Fatal("paced retry changed authority or content")
		}
		// Holding the writer again must not enqueue duplicate copies.
		quiet()
		if err := e.Written(epoch, retry.Frame); err != nil {
			t.Fatal(err)
		}
		barrier(t, e)
	}
	if err := e.Submit(epoch, control(WithdrawAckFrame, o, 0, ReasonDeclined)); err != nil {
		t.Fatal(err)
	}
	barrier(t, e)
	elapsed.Add(int64(time.Hour))
	quiet()
	if record, _ := r.Get(o.ID); record.PendingWithdrawal || len(e.withdrawSent) != 0 {
		t.Fatal("durable receipt did not retire its retry slot")
	}
}
