// Package lifecycle coordinates the native window without storing contact data.
package lifecycle

import "sync"

type Actions struct {
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
	mu                                     sync.Mutex
	actions                                Actions
	ready, pendingFocus, closing, quitting bool
}

func New(actions Actions) *Controller { return &Controller{actions: actions} }

func (c *Controller) Ready() {
	c.mu.Lock()
	c.ready = true
	focus := c.pendingFocus && !c.quitting
	c.pendingFocus = false
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
	if !c.ready {
		c.pendingFocus = true
		c.mu.Unlock()
		return
	}
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
