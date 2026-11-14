package desktop

import (
	"sync"

	"whatsup/internal/messaging"
)

const ContactChangedEvent = "whatsup:contact-changed"

// contactEvents coalesces public revision hints. Enqueue never calls application
// hooks and never acquires Service.mu. A blocked renderer cannot grow this queue
// with message content or an unbounded number of contact IDs.
type contactEvents struct {
	mu      sync.Mutex
	pending map[string]messaging.Hint
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
	closed  bool
}

func newContactEvents(sink func(messaging.Hint)) *contactEvents {
	if sink == nil {
		return nil
	}
	e := &contactEvents{pending: make(map[string]messaging.Hint), wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(e.done)
		for {
			select {
			case <-e.stop:
				return
			case <-e.wake:
			}
			e.mu.Lock()
			batch := e.pending
			e.pending = make(map[string]messaging.Hint)
			e.mu.Unlock()
			for _, hint := range batch {
				select {
				case <-e.stop:
					return
				default:
				}
				sink(hint)
			}
		}
	}()
	return e
}

func (e *contactEvents) enqueue(hint messaging.Hint) {
	if e == nil || hint.ContactID == "" || hint.Revision == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	old, exists := e.pending[hint.ContactID]
	if !exists && len(e.pending) >= 128 {
		return
	}
	if !exists || old.Revision < hint.Revision {
		e.pending[hint.ContactID] = hint
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *contactEvents) close() {
	if e == nil {
		return
	}
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		e.pending = nil
		close(e.stop)
	}
	e.mu.Unlock()
	<-e.done
}
