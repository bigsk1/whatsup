package desktop

import "sync"

const PairingChangedEvent = "whatsup:pairing-changed"

// PairingHint contains no identity, invitation or approval authority. Consumers
// reload GetPairingStatus after a hint or a visibility/network recovery.
type PairingHint struct {
	Revision uint64 `json:"revision"`
}

type pairingEvents struct {
	mu               sync.Mutex
	sink             func(PairingHint)
	revision         uint64
	closed           bool
	wake, stop, done chan struct{}
}

func newPairingEvents(sink func(PairingHint)) *pairingEvents {
	p := &pairingEvents{sink: sink, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go p.run()
	return p
}
func (p *pairingEvents) setSink(sink func(PairingHint)) {
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()
}
func (p *pairingEvents) enqueue(PairingHint) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.revision++
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *pairingEvents) run() {
	defer close(p.done)
	for {
		select {
		case <-p.stop:
			return
		case <-p.wake:
			p.mu.Lock()
			callback, revision, closed := p.sink, p.revision, p.closed
			p.mu.Unlock()
			if !closed && callback != nil {
				callback(PairingHint{Revision: revision})
			}
		}
	}
}
func (p *pairingEvents) close() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.stop)
	}
	p.mu.Unlock()
	<-p.done
}
func (c *invitationControl) changed() {
	if c != nil && c.onChanged != nil {
		c.onChanged()
	}
}
