package lifecycle

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// TrayMonitor keeps external desktop-bus checks off lifecycle locks and native
// focus callbacks. Only one bounded probe runs at a time; samples refresh while
// hidden too, so losing the tray cannot permanently strand the window.
type TrayMonitor struct {
	ctx                 context.Context
	probe               func(context.Context) bool
	unavailable         func()
	interval, timeLimit time.Duration
	wake                chan struct{}
	confirm             chan trayConfirmation
	done                chan struct{}
	start               sync.Once
	available           atomic.Bool
}

type trayConfirmation struct {
	ctx  context.Context
	done chan bool
}

func NewTrayMonitor(ctx context.Context, probe func(context.Context) bool, unavailable func()) *TrayMonitor {
	return &TrayMonitor{ctx: ctx, probe: probe, unavailable: unavailable, interval: 5 * time.Second, timeLimit: time.Second, wake: make(chan struct{}, 1), confirm: make(chan trayConfirmation), done: make(chan struct{})}
}

func (m *TrayMonitor) Start() {
	if m == nil {
		return
	}
	m.start.Do(func() { go m.run() })
}

func (m *TrayMonitor) Refresh() {
	if m == nil {
		return
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *TrayMonitor) Available() bool {
	if m == nil {
		return false
	}
	m.Refresh()
	return m.available.Load()
}

// Fresh confirms a negative/unknown cached result before a close decision. It
// waits outside controller/native UI locks and shares the single probe worker.
// The bound covers one already-running probe plus one newly requested probe.
func (m *TrayMonitor) Fresh(ctx context.Context) bool {
	if m == nil || ctx == nil || ctx.Err() != nil || m.ctx.Err() != nil {
		return false
	}
	m.Start()
	bounded, cancel := context.WithTimeout(ctx, 2*m.timeLimit)
	defer cancel()
	request := trayConfirmation{ctx: bounded, done: make(chan bool, 1)}
	select {
	case <-bounded.Done():
		return false
	case <-m.ctx.Done():
		return false
	case <-m.done:
		return false
	case m.confirm <- request:
	}
	select {
	case result := <-request.done:
		return result
	case <-bounded.Done():
		return false
	case <-m.ctx.Done():
		return false
	case <-m.done:
		return false
	}
}

func (m *TrayMonitor) sample(parent context.Context) bool {
	if parent.Err() != nil || m.ctx.Err() != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, m.timeLimit)
	stop := context.AfterFunc(m.ctx, cancel)
	available := m.probe != nil && m.probe(ctx)
	stop()
	cancel()
	if parent.Err() != nil || m.ctx.Err() != nil {
		return false
	}
	m.available.Store(available)
	if !available && m.unavailable != nil {
		m.unavailable()
	}
	return available
}

func (m *TrayMonitor) run() {
	defer close(m.done)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	m.sample(m.ctx)
	for {
		if m.ctx.Err() != nil {
			return
		}
		// Prioritize an explicit close confirmation over a queued background
		// refresh. Neither request can create another concurrent probe.
		select {
		case request := <-m.confirm:
			request.done <- m.sample(request.ctx)
			continue
		default:
		}
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.sample(m.ctx)
		case <-m.wake:
			m.sample(m.ctx)
		case request := <-m.confirm:
			request.done <- m.sample(request.ctx)
		}
	}
}
