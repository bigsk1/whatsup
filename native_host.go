package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"whatsup/internal/desktop"
	"whatsup/internal/diagnostics"
	"whatsup/internal/launch"
	"whatsup/internal/lifecycle"
	"whatsup/internal/messaging"
	"whatsup/internal/notify"
	"whatsup/internal/preferences"
)

const nativeStorageChangedEvent = "whatsup:storage-changed"
const nativeWindowVisibleEvent = "whatsup:window-visible"
const nativeNotificationActivateEvent = "whatsup:notification-activate"

// NativeNotificationHint is a selection request, never storage authority.
// The renderer consumes the pending ID and verifies its unlocked contacts.
type NativeNotificationHint struct {
	ContactID string `json:"contactID"`
}

// NativeStorageHint carries no identity or permission. The frontend must fetch
// authoritative status after retiring its previous decrypted generation.
type NativeStorageHint struct {
	State              string `json:"state"`
	RequiresPassphrase bool   `json:"requiresPassphrase"`
}

type nativeNotifier interface {
	Start(context.Context) error
	Send(context.Context, notify.Notice) error
	Close() error
}

type nativeHost struct {
	app               *application.App
	backend           *desktop.Service
	record            func(diagnostics.Event)
	entry             launch.Entry
	notifier          nativeNotifier
	controller        *lifecycle.Controller
	tray              *application.SystemTray
	windowMu          sync.RWMutex
	window            *application.WebviewWindow
	closeToTray       atomic.Bool
	visible           atomic.Bool
	focused           atomic.Bool
	readyOnce         sync.Once
	warningOnce       sync.Once
	life              context.Context
	cancel            context.CancelFunc
	incoming          chan messaging.Incoming
	fileDrops         chan []string
	incomingDone      chan struct{}
	stopOnce          sync.Once
	startupMu         sync.Mutex
	startupSuppressed bool
	startupStarted    bool
	startupCancel     context.CancelFunc
	startupDone       chan struct{}
	offlineMu         sync.Mutex
	actionMu          sync.Mutex
	actions           sync.WaitGroup
	stopped           bool
	unlock            func() (desktop.Status, error)
	lock              func() desktop.Status
	emitStorage       func(string)
	visibilitySink    func(bool)
	activationMu      sync.Mutex
	activation        string
	activationSink    func(NativeNotificationHint)
	deliver           func(context.Context, messaging.Incoming) error
}

func newNativeTasks(record func(diagnostics.Event)) *nativeHost {
	if record == nil {
		record = func(diagnostics.Event) {}
	}
	life, cancel := context.WithCancel(context.Background())
	h := &nativeHost{record: record, life: life, cancel: cancel,
		incoming: make(chan messaging.Incoming, 64), incomingDone: make(chan struct{})}
	h.closeToTray.Store(preferences.Defaults().CloseToTray)
	go h.incomingLoop()
	return h
}

func newNativeHost(app *application.App, backend *desktop.Service, record func(diagnostics.Event), opts launch.Options, icon []byte) (*nativeHost, error) {
	if app == nil || backend == nil || len(icon) == 0 {
		return nil, errors.New("Native app integration is unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, errors.New("Native app integration is unavailable")
	}
	entry, err := launch.NewStartup(executable, opts.SoftwareRendering)
	if err != nil {
		return nil, errors.New("Native startup integration is unavailable")
	}
	h := newNativeTasks(record)
	h.app, h.backend, h.entry = app, backend, entry
	h.visibilitySink = func(eligible bool) { app.Event.Emit(nativeWindowVisibleEvent, eligible) }
	h.activationSink = func(hint NativeNotificationHint) { app.Event.Emit(nativeNotificationActivateEvent, hint) }
	h.unlock, h.lock = backend.UnlockStorage, backend.LockStorage
	h.emitStorage = func(state string) {
		if state != "locked" && state != "unlocked" {
			state = "unavailable"
		}
		if h.life.Err() == nil {
			app.Event.Emit(nativeStorageChangedEvent, NativeStorageHint{State: state, RequiresPassphrase: backend.GetStatus().RequiresPassphrase})
		}
	}
	h.notifier = notify.New(h.activateContact)
	h.deliver = func(ctx context.Context, incoming messaging.Incoming) error {
		return desktop.WithIncomingNotification(backend, incoming, func(value desktop.Notification) error {
			if ctx.Err() != nil || h.foreground() {
				return nil
			}
			return h.notifier.Send(ctx, notify.Notice{ID: incoming.MessageID, ContactID: incoming.ContactID, Title: value.Name, Body: value.Body})
		})
	}
	h.controller = lifecycle.New(lifecycle.Actions{
		StartHidden: opts.Autostart,
		Show:        h.show, Hide: h.hide,
		Quit:          func() { go func() { h.stopTasks(); app.Quit() }() },
		Background:    h.closeToTray.Load,
		TrayAvailable: func() bool { return h.tray != nil && nativeTrayAvailable() },
		NoticeSeen:    func() (bool, error) { return desktop.TrayNoticeSeen(backend) },
		Notice:        h.notice,
		MarkNotice: func() error {
			if h.life.Err() != nil {
				return h.life.Err()
			}
			return desktop.MarkTrayNoticeSeen(backend)
		},
	})
	menu := application.NewMenu()
	menu.Add("Show WhatsUp").OnClick(func(*application.Context) { h.focus() })
	menu.Add("Go offline").OnClick(func(*application.Context) { h.enqueueOffline() })
	menu.AddSeparator()
	menu.Add("Quit").OnClick(func(*application.Context) { h.controller.Quit() })
	h.tray = app.SystemTray.New().SetIcon(icon).SetMenu(menu)
	h.tray.SetTooltip("WhatsUp")
	h.tray.OnClick(h.focus).OnDoubleClick(h.focus)
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go h.nativeStarted()
	})
	return h, nil
}

// Native startup does not depend on the renderer injecting its bridge. The
// grace period permits tray registration, then reveals a hidden broken view.
func (h *nativeHost) nativeStarted() {
	if h.life.Err() != nil {
		return
	}
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	// Wails can emit ApplicationStarted before its deferred window Run. Wait
	// for the native handle, never for JavaScript or a successful navigation.
	for {
		if window := h.windowValue(); window != nil && application.InvokeSyncWithResult(func() bool { return window.NativeWindow() != nil }) {
			h.controller.NativeReady()
			break
		}
		select {
		case <-h.life.Done():
			return
		case <-timer.C:
			h.controller.NativeReady()
			h.controller.StartupFallback()
			return
		case <-ticker.C:
		}
	}
	select {
	case <-h.life.Done():
	case <-timer.C:
		h.controller.StartupFallback()
	}
}

func (h *nativeHost) windowValue() *application.WebviewWindow {
	h.windowMu.RLock()
	defer h.windowMu.RUnlock()
	return h.window
}

func (h *nativeHost) attachWindow(window *application.WebviewWindow) {
	if h == nil || window == nil {
		return
	}
	h.windowMu.Lock()
	h.window = window
	h.windowMu.Unlock()
	window.RegisterHook(events.Common.WindowClosing, func(event *application.WindowEvent) {
		if h.controller.Quitting() || h.life.Err() != nil {
			return
		}
		event.Cancel()
		go h.controller.CloseWindow()
	})
	window.OnWindowEvent(events.Common.WindowRuntimeReady, func(*application.WindowEvent) {
		h.readyOnce.Do(func() {
			// Seed read eligibility after runtime readiness. Physical visibility
			// alone does not mean this application owns foreground focus.
			h.focused.Store(window.IsFocused())
			h.setVisible(window.IsVisible() && !window.IsMinimised())
			h.controller.Ready()
			h.startStartup()
		})
	})
	window.OnWindowEvent(events.Common.WindowFocus, func(*application.WindowEvent) {
		h.focused.Store(true)
		h.setVisible(window.IsVisible() && !window.IsMinimised())
	})
	window.OnWindowEvent(events.Common.WindowLostFocus, func(*application.WindowEvent) {
		h.focused.Store(false)
		h.publishReadEligibility()
	})
	for _, kind := range []events.WindowEventType{events.Common.WindowHide, events.Common.WindowMinimise} {
		window.OnWindowEvent(kind, func(*application.WindowEvent) { h.focused.Store(false); h.setVisible(false) })
	}
	for _, kind := range []events.WindowEventType{events.Common.WindowShow, events.Common.WindowRestore, events.Common.WindowUnMinimise} {
		window.OnWindowEvent(kind, func(*application.WindowEvent) { h.setVisible(window.IsVisible() && !window.IsMinimised()) })
	}
}

func (h *nativeHost) setVisible(visible bool) {
	h.visible.Store(visible)
	h.publishReadEligibility()
}

// window-visible carries foreground read eligibility. Background windows leave
// messages unread even when the window remains physically visible.
func (h *nativeHost) publishReadEligibility() {
	if h.visibilitySink != nil && h.life.Err() == nil {
		h.visibilitySink(h.foreground())
	}
}

func (h *nativeHost) foreground() bool { return h.visible.Load() && h.focused.Load() }

func (h *nativeHost) show() {
	if h.life.Err() != nil {
		return
	}
	if window := h.windowValue(); window != nil {
		window.Show()
		// Pinned Wails' first Show may create a deferred hidden native window.
		// A second call explicitly reveals it after that synchronous creation.
		window.Show()
		window.UnMinimise()
		window.Focus()
		h.focused.Store(window.IsFocused())
		h.setVisible(window.IsVisible() && !window.IsMinimised())
	}
}

func (h *nativeHost) hide() {
	if h.life.Err() != nil {
		return
	}
	if window := h.windowValue(); window != nil {
		window.Hide()
		h.focused.Store(window.IsFocused())
		h.setVisible(window.IsVisible() && !window.IsMinimised())
	}
}

func (h *nativeHost) focus() {
	if h != nil && h.life.Err() == nil && h.controller != nil {
		h.controller.Focus()
	}
}

func (h *nativeHost) activateContact(contactID string) {
	if h == nil || !nativeIncomingID(contactID, true) {
		return
	}
	h.activationMu.Lock()
	if h.life.Err() != nil {
		h.activationMu.Unlock()
		return
	}
	// Only the latest request is needed. It remains available when the toast
	// callback runs before the renderer has subscribed to native events.
	h.activation = contactID
	h.activationMu.Unlock()
	if h.activationSink != nil {
		h.activationSink(NativeNotificationHint{ContactID: contactID})
	}
	go h.focus()
}

func (h *nativeHost) takeNotificationActivation() string {
	if h == nil {
		return ""
	}
	h.activationMu.Lock()
	defer h.activationMu.Unlock()
	id := h.activation
	h.activation = ""
	if h.life.Err() != nil {
		return ""
	}
	return id
}

func (h *nativeHost) notice(noTray bool) {
	if h.life.Err() != nil {
		return
	}
	message := "WhatsUp will keep running in the tray when you close this window. Messages can arrive while the window is hidden. Choose Quit from the tray to exit, or Go offline to disconnect and lock local storage."
	if noTray {
		message = "This desktop does not provide an available system tray. WhatsUp will quit instead of hiding an unreachable window. Both apps must be running to exchange messages."
	}
	ack := make(chan struct{})
	var once sync.Once
	dialog := h.app.Dialog.Info().SetTitle("WhatsUp").SetMessage(message)
	if window := h.windowValue(); window != nil {
		dialog.AttachToWindow(window)
	}
	dialog.AddButton("OK").SetAsDefault().SetAsCancel().OnClick(func() { once.Do(func() { close(ack) }) })
	dialog.Show()
	select {
	case <-ack:
	case <-h.life.Done():
	}
}

func (h *nativeHost) preferencesChanged(values desktop.AppPreferences) {
	h.closeToTray.Store(values.CloseToTray)
}

func (h *nativeHost) setStartAtLogin(enabled bool) error {
	if h == nil || h.entry == nil || h.life.Err() != nil {
		return launch.ErrStartup
	}
	return h.entry.Set(enabled)
}

func (h *nativeHost) reconcileStartup(values desktop.AppPreferences) error {
	// Reconcile only this app's fixed owned entry. Saved false must also remove
	// a stale enabled entry after an uncertain preferences transaction.
	return h.setStartAtLogin(values.StartAtLogin)
}

func nativeIncomingID(value string, nonzero bool) bool {
	if len(value) != 22 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != 16 {
		return false
	}
	if nonzero {
		var any byte
		for _, b := range decoded {
			any |= b
		}
		if any == 0 {
			return false
		}
	}
	return true
}

func (h *nativeHost) enqueueIncoming(value messaging.Incoming) {
	if h == nil || h.life.Err() != nil || h.foreground() || !nativeIncomingID(value.ContactID, true) || !nativeIncomingID(value.MessageID, false) {
		return
	}
	select {
	case <-h.life.Done():
	case h.incoming <- value:
	default:
	}
}

func (h *nativeHost) incomingLoop() {
	defer close(h.incomingDone)
	for {
		select {
		case <-h.life.Done():
			return
		case value := <-h.incoming:
			if h.life.Err() != nil {
				return
			}
			if h.foreground() || h.deliver == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(h.life, notify.CallTimeout)
			err := h.deliver(ctx, value)
			cancel()
			if err != nil && h.life.Err() == nil {
				h.warningOnce.Do(func() { h.record(diagnostics.RuntimeWarning) })
			}
		}
	}
}

func (h *nativeHost) startStartup() {
	h.startupMu.Lock()
	if h.startupStarted || h.startupSuppressed || h.life.Err() != nil {
		h.startupMu.Unlock()
		return
	}
	h.startupStarted = true
	ctx, cancel := context.WithCancel(h.life)
	done := make(chan struct{})
	h.startupCancel, h.startupDone = cancel, done
	h.startupMu.Unlock()
	go func() {
		defer close(done)
		if ctx.Err() != nil || h.unlock == nil {
			return
		}
		status, err := h.unlock()
		if ctx.Err() != nil {
			if h.lock != nil {
				h.lock()
			}
			return
		}
		if status.StorageState == "locked" && status.RequiresPassphrase {
			return
		}
		if err != nil {
			h.record(diagnostics.StorageUnavailable)
		}
		if status.StorageState != "locked" && status.StorageState != "unlocked" {
			status.StorageState = "unavailable"
		}
		if h.emitStorage != nil {
			h.emitStorage(status.StorageState)
		}
	}()
}

func (h *nativeHost) stopStartup() {
	h.startupMu.Lock()
	h.startupSuppressed = true
	if h.startupCancel != nil {
		h.startupCancel()
	}
	done := h.startupDone
	h.startupMu.Unlock()
	if done != nil {
		<-done
	}
}

func (h *nativeHost) enqueueOffline() {
	// Retire clicks queued before the explicit offline action. Later clicks may
	// request a window/selection, but cannot open storage or start networking.
	h.takeNotificationActivation()
	h.actionMu.Lock()
	if h.stopped {
		h.actionMu.Unlock()
		return
	}
	h.actions.Add(1)
	h.actionMu.Unlock()
	go func() {
		defer h.actions.Done()
		h.offlineMu.Lock()
		defer h.offlineMu.Unlock()
		h.stopStartup()
		if h.life.Err() != nil || h.lock == nil {
			return
		}
		status := h.lock()
		if h.emitStorage != nil {
			h.emitStorage(status.StorageState)
		}
	}()
}

func (h *nativeHost) stopTasks() {
	if h == nil {
		return
	}
	h.stopOnce.Do(func() {
		h.actionMu.Lock()
		h.stopped = true
		h.cancel()
		h.takeNotificationActivation()
		h.actionMu.Unlock()
		h.stopStartup()
		<-h.incomingDone
		h.actions.Wait()
	})
}

// nativeLifecycle exposes lifecycle hooks and one opaque selection request.
// It never exposes notification delivery or native-window operations.
type nativeLifecycle struct{ host *nativeHost }

// TakeNotificationActivation atomically consumes a pending notification click.
// It grants no access to storage, contacts or message content.
func (n *nativeLifecycle) TakeNotificationActivation() string {
	if n == nil {
		return ""
	}
	return n.host.takeNotificationActivation()
}

func (n *nativeLifecycle) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	h := n.host
	if h == nil || ctx == nil {
		return errors.New("Native app integration is unavailable")
	}
	context.AfterFunc(ctx, h.stopTasks)
	values, err := h.backend.GetAppPreferences()
	if err != nil {
		// Unreadable preferences must not prevent explicit recovery, and must
		// not reconcile unknown startup/notification choices to defaults.
		h.record(diagnostics.RuntimeWarning)
		return nil
	}
	h.preferencesChanged(values)
	if h.reconcileStartup(values) != nil {
		h.record(diagnostics.RuntimeWarning)
	}
	if h.notifier.Start(h.life) != nil {
		h.warningOnce.Do(func() { h.record(diagnostics.RuntimeWarning) })
	}
	return nil
}

func (n *nativeLifecycle) ServiceShutdown() error {
	if n == nil || n.host == nil {
		return nil
	}
	n.host.stopTasks()
	return n.host.notifier.Close()
}
