package main

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"whatsup/internal/desktop"
	"whatsup/internal/lifecycle"
	"whatsup/internal/messaging"
	"whatsup/internal/notify"
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

func TestNativeProtectedStartupLeavesOnePassphrasePromptToFrontend(t *testing.T) {
	h := newNativeTasks(nil)
	t.Cleanup(h.stopTasks)
	hints := make(chan string, 1)
	h.unlock = func() (desktop.Status, error) {
		return desktop.Status{StorageState: "locked", RequiresPassphrase: true}, context.Canceled
	}
	h.emitStorage = func(state string) { hints <- state }
	h.startStartup()
	h.startupMu.Lock()
	done := h.startupDone
	h.startupMu.Unlock()
	awaitNative(t, done)
	select {
	case <-hints:
		t.Fatal("expected protected startup emitted a duplicate lock hint")
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

type nativePurgeNotifier struct{ contacts []string }

func (*nativePurgeNotifier) Start(context.Context) error                       { return nil }
func (*nativePurgeNotifier) Send(context.Context, notify.Notice) error         { return nil }
func (*nativePurgeNotifier) SendApproved(context.Context, notify.Notice) error { return nil }
func (*nativePurgeNotifier) Close() error                                      { return nil }
func (n *nativePurgeNotifier) ForgetContact(_ context.Context, id string) error {
	n.contacts = append(n.contacts, id)
	return nil
}

func TestNativeForgetPurgesPendingActivationOnlyForSelectedContact(t *testing.T) {
	h := newNativeTasks(nil)
	t.Cleanup(h.stopTasks)
	notifier := &nativePurgeNotifier{}
	h.notifier = notifier
	first, second := "AQEBAQEBAQEBAQEBAQEBAQ", "AgICAgICAgICAgICAgICAg"
	h.activation = second
	h.forgetContact(first)
	if h.takeNotificationActivation() != second {
		t.Fatal("Forget cleared another contact's pending selection")
	}
	h.activation = first
	h.forgetContact(first)
	if h.takeNotificationActivation() != "" || len(notifier.contacts) != 2 {
		t.Fatal("Forget retained activation or omitted notifier purge")
	}
}

func TestTrayLossRevealsHiddenButNotMerelyMinimizedWindow(t *testing.T) {
	h := newNativeTasks(nil)
	t.Cleanup(h.stopTasks)
	shown := 0
	h.controller = lifecycle.New(lifecycle.Actions{Show: func() { shown++; h.hiddenToTray.Store(false) }})
	h.controller.NativeReady()
	h.hiddenToTray.Store(true)
	h.trayUnavailable()
	if shown != 0 {
		t.Fatal("initial tray absence bypassed startup grace")
	}
	h.trayGraceElapsed.Store(true)
	h.hiddenToTray.Store(false)
	h.trayUnavailable()
	if shown != 0 {
		t.Fatal("ordinary minimization was mistaken for an unreachable tray window")
	}
	h.hiddenToTray.Store(true)
	h.trayUnavailable()
	h.trayUnavailable()
	if shown != 1 {
		t.Fatal("lost tray did not reveal hidden window exactly once")
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

func TestNativeLifecycleExposesOnlyLifecycleAndOpaqueActivation(t *testing.T) {
	kind := reflect.TypeOf(&nativeLifecycle{})
	if kind.NumMethod() != 3 || kind.Method(0).Name != "ServiceShutdown" || kind.Method(1).Name != "ServiceStartup" || kind.Method(2).Name != "TakeNotificationActivation" {
		t.Fatal("native operations escaped the narrow lifecycle boundary")
	}
}

func TestNativeActivationRetainsLatestContactUntilTakenAndNeverUnlocks(t *testing.T) {
	h := newNativeTasks(nil)
	t.Cleanup(h.stopTasks)
	h.unlock = func() (desktop.Status, error) { t.Error("notification unlocked storage"); return desktop.Status{}, nil }
	h.lock = func() desktop.Status { t.Error("notification changed storage"); return desktop.Status{} }
	shown := make(chan struct{}, 4)
	h.controller = lifecycle.New(lifecycle.Actions{Show: func() { shown <- struct{}{} }})
	h.controller.Ready()
	var hints []NativeNotificationHint
	h.activationSink = func(value NativeNotificationHint) { hints = append(hints, value) }
	first, second := "AQEBAQEBAQEBAQEBAQEBAQ", "AgICAgICAgICAgICAgICAg"
	for _, invalid := range []string{"", "AAAAAAAAAAAAAAAAAAAAAA", "not a contact", "AQEBAQEBAQEBAQEBAQEBAR"} {
		h.activateContact(invalid)
	}
	bridge := &nativeLifecycle{host: h}
	if bridge.TakeNotificationActivation() != "" || len(hints) != 0 {
		t.Fatal("invalid contact became an activation")
	}
	h.activateContact(first)
	h.activateContact(second)
	if len(hints) != 2 || hints[0].ContactID != first || hints[1].ContactID != second {
		t.Fatal("canonical activation did not emit its narrow hint")
	}
	for range 2 {
		select {
		case <-shown:
		case <-time.After(3 * time.Second):
			t.Fatal("notification did not request the existing window")
		}
	}
	if bridge.TakeNotificationActivation() != second || bridge.TakeNotificationActivation() != "" {
		t.Fatal("pending activation was lost, duplicated or did not keep the latest click")
	}
	h.activateContact(first)
	var taken atomic.Int32
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			if bridge.TakeNotificationActivation() == first {
				taken.Add(1)
			}
		})
	}
	workers.Wait()
	if taken.Load() != 1 {
		t.Fatal("activation consumption was not atomic")
	}
	h.activateContact(first)
	h.stopTasks()
	h.activateContact(second)
	if bridge.TakeNotificationActivation() != "" {
		t.Fatal("stopped host retained or accepted an activation")
	}
	if (*nativeLifecycle)(nil).TakeNotificationActivation() != "" || (&nativeLifecycle{}).TakeNotificationActivation() != "" {
		t.Fatal("missing host returned an activation")
	}
}

func TestNativeOfflineRetiresPendingNotificationActivation(t *testing.T) {
	h := newNativeTasks(nil)
	t.Cleanup(h.stopTasks)
	h.activateContact("AQEBAQEBAQEBAQEBAQEBAQ")
	h.enqueueOffline()
	if h.takeNotificationActivation() != "" {
		t.Fatal("explicit offline retained an earlier notification target")
	}
}
