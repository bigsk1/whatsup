// Package lifecycle coordinates the native window without storing contact data.
package lifecycle

import "sync"

type Actions struct {
	StartHidden   bool
	Show          func()
	Hide          func()
	Quit          func()
	Background    func() bool
	TrayAvailable func() bool
	NoticeSeen    func() (bool, error)
	Notice        func(noTray bool)
	MarkNotice    func() error
}

type Controller struct {
	mu                                                            sync.Mutex
	actions                                                       Actions
	ready, nativeReady, revealed, pendingFocus, closing, quitting bool
}

func New(actions Actions) *Controller { return &Controller{actions: actions} }

// NativeReady is independent of renderer readiness: a stalled WebView must not
// prevent the tray or a second launch from revealing the native window.
func (c *Controller) NativeReady() {
	c.mu.Lock()
	c.nativeReady = true
	focus := !c.quitting && c.pendingFocus
	c.pendingFocus = false
	if focus {
		c.revealed = true
	}
	c.mu.Unlock()
	if focus {
		c.actions.Show()
	}
}

func (c *Controller) Ready() {
	c.mu.Lock()
	if c.ready {
		c.mu.Unlock()
		return
	}
	c.ready = true
	// Autostart creates a hidden window before native tray availability can
	// be known. Keep a window reachable if that desktop has no working tray.
	c.nativeReady = true
	focus := !c.quitting && !c.revealed && (c.pendingFocus || c.actions.StartHidden && !c.actions.TrayAvailable())
	c.pendingFocus = false
	if focus {
		c.revealed = true
	}
	c.mu.Unlock()
	if focus {
		c.actions.Show()
	}
}

// StartupFallback is called after the bounded hidden-start grace period. A
// missing renderer-ready event or an unregistered tray fails visibly.
func (c *Controller) StartupFallback() {
	c.mu.Lock()
	focus := c.actions.StartHidden && c.nativeReady && !c.quitting && !c.revealed && (!c.ready || !c.actions.TrayAvailable())
	if focus {
		c.revealed = true
	}
	c.mu.Unlock()
	if focus {
		c.actions.Show()
	}
}

func (c *Controller) Focus() {
	c.mu.Lock()
	if c.quitting {
		c.mu.Unlock()
		return
	}
	if !c.nativeReady {
		c.pendingFocus = true
		c.mu.Unlock()
		return
	}
	c.revealed = true
	c.mu.Unlock()
	c.actions.Show()
}

func (c *Controller) Quitting() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.quitting }

func (c *Controller) Quit() {
	c.mu.Lock()
	if c.quitting {
		c.mu.Unlock()
		return
	}
	c.quitting = true
	c.mu.Unlock()
	c.actions.Quit()
}

// CloseWindow runs after the native close event was cancelled. It can display
// the first-use notice without holding any runtime or service guard.
func (c *Controller) CloseWindow() {
	c.mu.Lock()
	if c.quitting || c.closing {
		c.mu.Unlock()
		return
	}
	c.closing = true
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.closing = false; c.mu.Unlock() }()
	background := c.actions.Background()
	tray := c.actions.TrayAvailable()
	if !background || !tray {
		if background && !tray {
			c.actions.Notice(true)
		}
		c.Quit()
		return
	}
	seen, err := c.actions.NoticeSeen()
	if err != nil {
		return
	} // Keep the window reachable if policy is uncertain.
	if !seen {
		c.actions.Notice(false)
		if c.Quitting() || c.actions.MarkNotice() != nil {
			return
		}
	}
	if !c.Quitting() {
		c.actions.Hide()
	}
}
