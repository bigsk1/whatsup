package lifecycle

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrayMonitorRefreshesAfterInitialAbsenceAndLaterLoss(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var present atomic.Bool
	absent := make(chan struct{}, 8)
	monitor := NewTrayMonitor(ctx, func(context.Context) bool { return present.Load() }, func() { absent <- struct{}{} })
	monitor.interval = time.Hour
	monitor.Start()
	select {
	case <-absent:
	case <-time.After(time.Second):
		t.Fatal("initial probe missing")
	}
	if monitor.available.Load() {
		t.Fatal("missing tray reported reachable")
	}
	present.Store(true)
	monitor.Refresh()
	deadline := time.Now().Add(time.Second)
	for !monitor.available.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !monitor.available.Load() {
		t.Fatal("late tray registration never refreshed")
	}
	present.Store(false)
	monitor.Refresh()
	select {
	case <-absent:
	case <-time.After(time.Second):
		t.Fatal("loss of tray did not request visibility fallback")
	}
	if monitor.available.Load() {
		t.Fatal("stale available sample survived refresh")
	}
	cancel()
	select {
	case <-monitor.done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop")
	}
}

func TestTrayMonitorCachedChecksDoNotWaitForBlockedProbe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 1)
	monitor := NewTrayMonitor(ctx, func(ctx context.Context) bool { entered <- struct{}{}; <-ctx.Done(); return false }, nil)
	monitor.timeLimit = 20 * time.Millisecond
	monitor.Start()
	<-entered
	returned := make(chan struct{})
	go func() { _ = monitor.Available(); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("cached availability waited on desktop bus")
	}
	cancel()
	select {
	case <-monitor.done:
	case <-time.After(time.Second):
		t.Fatal("bounded probe ignored lifecycle cancellation")
	}
}
