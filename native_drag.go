package main

import (
	"context"
	"sync"
)

const nativeFileDragEvent = "whatsup:file-drag"
const nativeFileDragLastRevision uint64 = 1<<53 - 1

// NativeFileDragHint carries no file metadata or staging authority. Revision
// retires older in-flight hints when native drag state is reset or the app hides.
type NativeFileDragHint struct {
	Active   bool   `json:"active"`
	Revision uint64 `json:"revision"`
}

type nativeDragPublisher struct {
	mu           sync.Mutex
	dispatchMu   sync.Mutex
	ctx          context.Context
	cancel       context.CancelFunc
	sourceSerial uint64
	latest       NativeFileDragHint
	armed        bool
	outstanding  uint64
	emitted      uint64
	closed       bool
	wake         chan struct{}
	done         chan struct{}
	emit         func(NativeFileDragHint)
}

func newNativeDragPublisher(life context.Context, emit func(NativeFileDragHint)) *nativeDragPublisher {
	ctx, cancel := context.WithCancel(life)
	p := &nativeDragPublisher{ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}), emit: emit}
	go func() {
		defer close(p.done)
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
				// Only the renderer's acknowledgement makes another notification
				// eligible. A stalled renderer therefore leaves at most one drag
				// event in Wails' otherwise unbounded frontend mailbox.
				p.dispatchMu.Lock()
				p.mu.Lock()
				hint := p.latest
				ready := !p.closed && ctx.Err() == nil && p.armed && p.outstanding == 0 && hint.Revision > p.emitted && emit != nil
				if ready {
					p.outstanding = hint.Revision
				}
				p.mu.Unlock()
				if ready {
					emit(hint)
					p.mu.Lock()
					p.emitted = hint.Revision
					p.mu.Unlock()
				}
				p.dispatchMu.Unlock()
			}
		}
	}()
	return p
}

func (p *nativeDragPublisher) native(active bool, serial uint64) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil || serial == 0 || serial <= p.sourceSerial || serial > nativeFileDragLastRevision {
		return
	}
	p.sourceSerial = serial
	p.updateLocked(active)
}

func (p *nativeDragPublisher) updateLocked(active bool) {
	if p.latest.Revision >= nativeFileDragLastRevision {
		return
	}
	p.latest.Revision++
	if p.latest.Revision == nativeFileDragLastRevision {
		active = false
	}
	p.latest.Active = active
	p.wakeLocked()
}

func (p *nativeDragPublisher) wakeLocked() {
	if !p.armed || p.outstanding != 0 || p.latest.Revision <= p.emitted {
		return
	}
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// take arms delivery only after the renderer installs its event listener. Zero
// reads the latest snapshot without acknowledging anything. Other reads release
// only the exact notification that was actually emitted, never a newer snapshot
// revision that the renderer happened to read before dispatch.
func (p *nativeDragPublisher) take(ackRevision uint64) NativeFileDragHint {
	if p == nil {
		return NativeFileDragHint{}
	}
	if ackRevision != 0 {
		// Event.Emit only enqueues a scalar hint. Serialize acknowledgements
		// with that enqueue so an immediate event handler cannot race its
		// completion. Native callbacks never acquire this dispatch gate.
		p.dispatchMu.Lock()
		defer p.dispatchMu.Unlock()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil {
		return NativeFileDragHint{}
	}
	if ackRevision == 0 {
		p.armed = true
	}
	if ackRevision != 0 && ackRevision == p.outstanding && ackRevision == p.emitted {
		p.outstanding = 0
	}
	p.wakeLocked()
	return p.latest
}

func (p *nativeDragPublisher) clear() {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed && p.ctx.Err() == nil {
		p.updateLocked(false)
	}
}

func (p *nativeDragPublisher) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.closed = true
	p.latest.Active = false
	p.mu.Unlock()
	p.cancel()
	<-p.done
}

func (h *nativeHost) nativeFileDrag(active bool, serial uint64) {
	if h != nil && h.life.Err() == nil {
		h.drag.native(active, serial)
	}
}

func (h *nativeHost) resetNativeDrag() {
	if h == nil {
		return
	}
	if window := h.windowValue(); window != nil {
		window.ResetNativeFileDrag()
	} else {
		h.drag.clear()
	}
}
