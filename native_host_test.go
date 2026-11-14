package main

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/desktop"
	"whatsup/internal/messaging"
	"whatsup/internal/preferences"
)

func awaitNative(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("native task did not complete")
	}
}

func TestNativeOfflineJoinsStartupBeforeLockAndNeverEmitsLateUnlock(t *testing.T) {
	h := newNativeTasks(nil)
	t.Cleanup(h.stopTasks)
	entered, release := make(chan struct{}), make(chan struct{})
	var locked atomic.Bool
	var unlocks atomic.Int32
	h.unlock = func() (desktop.Status, error) {
		unlocks.Add(1)
		close(entered)
		<-release
		locked.Store(false)
		return desktop.Status{StorageState: "unlocked"}, nil
	}
	h.lock = func() desktop.Status { locked.Store(true); return desktop.Status{StorageState: "locked"} }
	hints := make(chan string, 4)
	h.emitStorage = func(state string) { hints <- state }
	h.startStartup()
	awaitNative(t, entered)
	h.enqueueOffline()
	select {
	case <-hints:
		t.Fatal("offline completed while startup could still unlock")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case state := <-hints:
		if state != "locked" {
			t.Fatal("a retired startup emitted unlocked")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("offline did not complete after startup joined")
	}
	if !locked.Load() {
		t.Fatal("late startup revived local storage")
	}
	h.startStartup()
	if unlocks.Load() != 1 {
		t.Fatal("startup ran again after explicit offline")
	}
	select {
	case <-hints:
		t.Fatal("extra late startup hint")
	default:
	}
}

func TestNativeIncomingQueueIsBoundedAndShutdownCancelsDelivery(t *testing.T) {
	h := newNativeTasks(nil)
	entered := make(chan struct{})
	var deliveries atomic.Int32
	h.deliver = func(ctx context.Context, _ messaging.Incoming) error {
		if deliveries.Add(1) == 1 {
			close(entered)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	value := messaging.Incoming{ContactID: "AQEBAQEBAQEBAQEBAQEBAQ", MessageID: "AAAAAAAAAAAAAAAAAAAAAA"}
	h.enqueueIncoming(value)
	awaitNative(t, entered)
	for range 1000 {
		h.enqueueIncoming(value)
	}
	if len(h.incoming) != 64 {
		t.Fatal("incoming queue exceeded or lost its fixed bound")
	}
	stopped := make(chan struct{})
	go func() { h.stopTasks(); close(stopped) }()
	awaitNative(t, stopped)
	if deliveries.Load() != 1 {
		t.Fatal("shutdown delivered queued notices after cancellation")
	}
	before := len(h.incoming)
	h.enqueueIncoming(value)
	if len(h.incoming) != before {
		t.Fatal("shutdown accepted a new notice")
	}
}

func TestNativeForegroundSuppressesIncomingWithoutDecryptingContent(t *testing.T) {
	h := newNativeTasks(nil)
	t.Cleanup(h.stopTasks)
	var deliveries atomic.Int32
	h.deliver = func(context.Context, messaging.Incoming) error { deliveries.Add(1); return nil }
	h.visible.Store(true)
	h.focused.Store(true)
	h.enqueueIncoming(messaging.Incoming{ContactID: "AQEBAQEBAQEBAQEBAQEBAQ", MessageID: "AAAAAAAAAAAAAAAAAAAAAA"})
	h.enqueueIncoming(messaging.Incoming{ContactID: "not a contact", MessageID: "private text is not an ID"})
	if deliveries.Load() != 0 || len(h.incoming) != 0 {
		t.Fatal("foreground or invalid notice reached the content lookup")
	}
}

type nativeTestEntry struct{ values []bool }

func TestNativeReadEligibilityRequiresFocusEvenWhenWindowIsVisible(t *testing.T) {
	h := newNativeTasks(nil)
	t.Cleanup(h.stopTasks)
	var eligibility []bool
	h.visibilitySink = func(value bool) { eligibility = append(eligibility, value) }
	h.setVisible(true)
	if !h.visible.Load() || h.foreground() || len(eligibility) != 1 || eligibility[0] {
		t.Fatal("a visible unfocused window was allowed to mark incoming messages read")
	}
	h.focused.Store(true)
	h.publishReadEligibility()
	h.focused.Store(false)
	h.publishReadEligibility()
	if !h.visible.Load() || len(eligibility) != 3 || !eligibility[1] || eligibility[2] {
		t.Fatal("native focus loss did not retire read eligibility independently of physical visibility")
	}
}

func (e *nativeTestEntry) Set(value bool) error { e.values = append(e.values, value); return nil }

func TestNativeStartupReconcilesOwnedEntryWithoutEnablingDefaults(t *testing.T) {
	h := newNativeTasks(nil)
	t.Cleanup(h.stopTasks)
	entry := &nativeTestEntry{}
	h.entry = entry
	h.preferencesChanged(preferences.Defaults())
	if len(entry.values) != 0 || !h.closeToTray.Load() {
		t.Fatal("loading defaults changed an OS startup entry")
	}
	if h.reconcileStartup(preferences.Defaults()) != nil || len(entry.values) != 1 || entry.values[0] {
		t.Fatal("saved false was not reconciled without enabling startup")
	}
	next := preferences.Defaults()
	next.CloseToTray = false
	next.StartAtLogin = true
	h.preferencesChanged(next)
	if len(entry.values) != 1 || h.closeToTray.Load() {
		t.Fatal("preference callback added an implicit OS write")
	}
	if h.setStartAtLogin(true) != nil || len(entry.values) != 2 || !entry.values[1] {
		t.Fatal("explicit startup opt-in was not applied")
	}
	if h.reconcileStartup(preferences.Defaults()) != nil || len(entry.values) != 3 || entry.values[2] {
		t.Fatal("saved false did not remove a stale owned startup opt-in")
	}
	h.stopTasks()
	if h.setStartAtLogin(false) == nil || len(entry.values) != 3 {
		t.Fatal("stopped native host wrote startup configuration")
	}
}

func TestNativeLifecycleHasOnlyLifecycleMethods(t *testing.T) {
	kind := reflect.TypeOf(&nativeLifecycle{})
	if kind.NumMethod() != 2 || kind.Method(0).Name != "ServiceShutdown" || kind.Method(1).Name != "ServiceStartup" {
		t.Fatal("native operations escaped the lifecycle-only boundary")
	}
}
