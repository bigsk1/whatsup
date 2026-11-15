package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloseRechecksNegativeCacheAfterTrayRegisters(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var present atomic.Bool
	initial, entered, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var initialOnce, enteredOnce sync.Once
	monitor := NewTrayMonitor(ctx, func(ctx context.Context) bool {
		if !present.Load() {
			return false
		}
		enteredOnce.Do(func() { close(entered) })
		select {
		case <-release:
			return true
		case <-ctx.Done():
			return false
		}
	}, func() { initialOnce.Do(func() { close(initial) }) })
	monitor.interval = time.Hour
	monitor.Start()
	<-initial
	present.Store(true)
	actions := make(chan string, 2)
	c := New(Actions{TrayAvailable: monitor.Available, ConfirmTrayAvailable: func() bool { return monitor.Fresh(ctx) }, Background: func() bool { return true },
		Hide: func() { actions <- "hide" }, Quit: func() { actions <- "quit" }, Notice: func(bool) {}, NoticeSeen: func() (bool, error) { return true, nil }})
	c.NativeReady()
	go c.CloseWindow()
	select {
	case <-entered:
		close(release)
	case <-time.After(time.Second):
		t.Fatal("close did not request refreshed tray proof")
	}
	select {
	case action := <-actions:
		if action != "hide" {
			t.Fatal("close quit on cached absence although tray had registered")
		}
	case <-time.After(time.Second):
		t.Fatal("close did not finish after fresh tray proof")
	}
}

func TestCloseFreshProbeDoesNotHoldControllerFocusOrQuit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	initial, entered := make(chan struct{}), make(chan struct{})
	var count atomic.Int32
	var once sync.Once
	monitor := NewTrayMonitor(ctx, func(ctx context.Context) bool {
		if count.Add(1) == 1 {
			return false
		}
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return false
	}, func() {
		select {
		case <-initial:
		default:
			close(initial)
		}
	})
	monitor.interval = time.Hour
	monitor.Start()
	<-initial
	shown, quit, closed := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
	c := New(Actions{TrayAvailable: monitor.Available, ConfirmTrayAvailable: func() bool { return monitor.Fresh(ctx) }, Background: func() bool { return true },
		Show: func() { shown <- struct{}{} }, Quit: func() { cancel(); quit <- struct{}{} }, Notice: func(bool) { t.Error("late close notice after Quit") }})
	c.NativeReady()
	go func() { c.CloseWindow(); close(closed) }()
	<-entered
	go c.Focus()
	select {
	case <-shown:
	case <-time.After(time.Second):
		t.Fatal("focus blocked on fresh tray probe")
	}
	go c.Quit()
	select {
	case <-quit:
	case <-time.After(time.Second):
		t.Fatal("Quit blocked on fresh tray probe")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not unblock close confirmation")
	}
}

func TestCloseKeepsPositiveCacheFastPath(t *testing.T) {
	hidden := false
	c := New(Actions{TrayAvailable: func() bool { return true }, ConfirmTrayAvailable: func() bool { t.Fatal("positive cache unnecessarily blocked close"); return false }, Background: func() bool { return true }, Hide: func() { hidden = true }, NoticeSeen: func() (bool, error) { return true, nil }})
	c.CloseWindow()
	if !hidden {
		t.Fatal("positive tray close did not hide window")
	}
}

func TestFreshTrayConfirmationIsBoundedAndKeepsOneProbeInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active, maximum atomic.Int32
	monitor := NewTrayMonitor(ctx, func(ctx context.Context) bool {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := maximum.Load(); current > previous; previous = maximum.Load() {
			if maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		<-ctx.Done()
		return false
	}, nil)
	monitor.timeLimit = 10 * time.Millisecond
	monitor.interval = time.Hour
	started := time.Now()
	var clients sync.WaitGroup
	for range 10 {
		clients.Go(func() {
			if monitor.Fresh(context.Background()) {
				t.Error("unreachable host confirmed")
			}
		})
	}
	clients.Wait()
	if time.Since(started) > time.Second || maximum.Load() != 1 {
		t.Fatal("confirmation unbounded or created concurrent probes")
	}
	cancel()
	select {
	case <-monitor.done:
	case <-time.After(time.Second):
		t.Fatal("probe worker did not stop")
	}
}
