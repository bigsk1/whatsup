package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestNativeDragPublisherCoalescesAndRejectsRetiredCallbacks(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	out := make(chan NativeFileDragHint, 4)
	first := true
	p := newNativeDragPublisher(context.Background(), func(hint NativeFileDragHint) {
		if first {
			first = false
			close(entered)
			<-release
		}
		out <- hint
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		p.close()
	}()
	p.take(0)
	p.native(true, 1)
	<-entered
	p.native(false, 2)
	p.native(true, 1) // Callback generated before the reset, delivered afterward.
	p.mu.Lock()
	latest, serial := p.latest, p.sourceSerial
	p.mu.Unlock()
	if latest.Active || latest.Revision != 2 || serial != 2 {
		t.Fatal("stale native callback revived a retired highlight")
	}
	for index := uint64(3); index < 10003; index++ {
		p.native(index%2 != 0, index)
	}
	if len(p.wake) != 0 {
		t.Fatal("native burst scheduled another notification before acknowledgement")
	}
	if hint := p.take(0); hint.Active || hint.Revision != 10002 {
		t.Fatal("snapshot read waited for dispatch or missed the coalesced latest state")
	}
	close(release)
	if hint := receiveNativeDrag(t, out); hint.Revision != 1 || !hint.Active {
		t.Fatal("initial notification changed while it was being dispatched")
	}
	// Reading the newest snapshot, or acknowledging its revision before that
	// revision was emitted, must not release the old queued notification.
	if hint := p.take(10002); hint.Active || hint.Revision != 10002 {
		t.Fatal("read-before-dispatch did not return the current snapshot")
	}
	p.mu.Lock()
	outstanding, emitted := p.outstanding, p.emitted
	p.mu.Unlock()
	if outstanding != 1 || emitted != 1 {
		t.Fatal("snapshot revision acknowledged a notification that was never emitted")
	}
	select {
	case <-out:
		t.Fatal("stalled frontend allowed another Wails mailbox entry")
	default:
	}
	if hint := p.take(1); hint.Active || hint.Revision != 10002 {
		t.Fatal("acknowledgement failed to return the latest state")
	}
	if hint := receiveNativeDrag(t, out); hint.Revision != 10002 || hint.Active {
		t.Fatal("coalesced inactive transition was lost after acknowledgement")
	}
	p.take(1) // An old event handler cannot acknowledge the newer notification.
	p.mu.Lock()
	final, outstanding := p.latest, p.outstanding
	p.mu.Unlock()
	if final.Active || final.Revision != 10002 || outstanding != 10002 {
		t.Fatal("stale acknowledgement released the current outstanding notification")
	}
	p.take(10002)
	p.mu.Lock()
	outstanding = p.outstanding
	p.mu.Unlock()
	if outstanding != 0 {
		t.Fatal("exact acknowledgement did not release the emitted notification")
	}
}

func receiveNativeDrag(t *testing.T, out <-chan NativeFileDragHint) NativeFileDragHint {
	t.Helper()
	select {
	case hint := <-out:
		return hint
	case <-time.After(time.Second):
		t.Fatal("native drag notification did not publish")
		return NativeFileDragHint{}
	}
}

func TestNativeDragWaitsForListenerArmingAndBoundsDispatchedBurst(t *testing.T) {
	out := make(chan NativeFileDragHint, 4)
	p := newNativeDragPublisher(context.Background(), func(hint NativeFileDragHint) { out <- hint })
	defer p.close()
	for serial := uint64(1); serial <= 10000; serial++ {
		p.native(serial%2 != 0, serial)
	}
	p.take(10000) // An old/guessed acknowledgement does not arm a new listener.
	if len(p.wake) != 0 || len(out) != 0 {
		t.Fatal("native state emitted before the renderer installed its listener")
	}
	if hint := p.take(0); hint.Active || hint.Revision != 10000 {
		t.Fatal("startup read lost state accumulated before the listener existed")
	}
	if hint := receiveNativeDrag(t, out); hint.Active || hint.Revision != 10000 {
		t.Fatal("arming did not deliver the latest native state")
	}
	for serial := uint64(10001); serial <= 20000; serial++ {
		p.native(serial%2 != 0, serial)
	}
	// Take(0) is safe for repeated runtime-ready/startup reads, but cannot
	// release a notification that its event handler has not acknowledged.
	p.take(0)
	p.take(nativeFileDragLastRevision)
	p.mu.Lock()
	outstanding, latest := p.outstanding, p.latest
	p.mu.Unlock()
	if outstanding != 10000 || latest.Revision != 20000 || len(out) != 0 || len(p.wake) != 0 {
		t.Fatal("stalled renderer did not retain exactly one unacknowledged notification")
	}
	p.take(10000)
	if hint := receiveNativeDrag(t, out); hint.Active || hint.Revision != 20000 {
		t.Fatal("renderer acknowledgement did not resume coalesced delivery")
	}
}

func TestNativeDragImmediateAcknowledgementWaitsForActualDispatch(t *testing.T) {
	out := make(chan NativeFileDragHint, 2)
	entered, release := make(chan struct{}), make(chan struct{})
	first := true
	p := newNativeDragPublisher(context.Background(), func(hint NativeFileDragHint) {
		out <- hint
		if first {
			first = false
			close(entered)
			<-release
		}
	})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		p.close()
	}()
	p.take(0)
	p.native(true, 1)
	<-entered
	hint := receiveNativeDrag(t, out)
	acknowledged := make(chan NativeFileDragHint, 1)
	go func() { acknowledged <- p.take(hint.Revision) }()
	p.native(false, 2)
	// Native UI callbacks and startup snapshots stay responsive while an
	// event handler is acknowledging an enqueue that has not returned yet.
	if latest := p.take(0); latest.Active || latest.Revision != 2 {
		t.Fatal("pending dispatch blocked or lost the latest native state")
	}
	select {
	case <-acknowledged:
		t.Fatal("acknowledgement retired an event before actual dispatch completed")
	default:
	}
	close(release)
	select {
	case latest := <-acknowledged:
		if latest.Active || latest.Revision != 2 {
			t.Fatal("immediate event acknowledgement lost the latest coalesced state")
		}
	case <-time.After(time.Second):
		t.Fatal("event acknowledgement was lost while dispatch completed")
	}
	if latest := receiveNativeDrag(t, out); latest.Active || latest.Revision != 2 {
		t.Fatal("matching immediate acknowledgement did not permit the next notification")
	}
}

func TestNativeDragLifecycleSnapshotIsDisplayOnlyAndNilSafe(t *testing.T) {
	var missing *nativeLifecycle
	for _, bridge := range []*nativeLifecycle{missing, {}, {host: &nativeHost{}}} {
		if got := bridge.TakeFileDragHint(0); got != (NativeFileDragHint{}) {
			t.Fatal("missing native publisher exposed a nonempty hint")
		}
	}
	p := newNativeDragPublisher(context.Background(), nil)
	defer p.close()
	bridge := &nativeLifecycle{host: &nativeHost{drag: p}}
	if got := bridge.TakeFileDragHint(0); got != (NativeFileDragHint{}) {
		t.Fatal("empty startup snapshot was not the inactive revision-zero sentinel")
	}
	p.native(true, 1)
	if got := bridge.TakeFileDragHint(0); got != (NativeFileDragHint{Active: true, Revision: 1}) {
		t.Fatal("native lifecycle changed the scalar drag snapshot")
	}
}

func TestNativeDragHintContainsNoFileDataAndStopsWithHostLife(t *testing.T) {
	life, cancel := context.WithCancel(context.Background())
	p := newNativeDragPublisher(life, nil)
	p.native(true, 1)
	data, err := json.Marshal(p.latest)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if json.Unmarshal(data, &fields) != nil || len(fields) != 2 || fields["active"] != true || fields["revision"] != float64(1) {
		t.Fatal("native drag hint leaked payload or permission")
	}
	p.clear()
	p.mu.Lock()
	retired := p.latest
	p.mu.Unlock()
	if retired.Active || retired.Revision != 2 {
		t.Fatal("storage/window retirement did not clear drag state")
	}
	cancel()
	p.close()
	p.native(true, 2)
	p.mu.Lock()
	final := p.latest
	p.mu.Unlock()
	if final.Active || final.Revision != 2 {
		t.Fatal("stopped host accepted a stale drag callback")
	}
}
