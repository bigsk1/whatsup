// Package notify owns narrow native message notices. It is never bound to the
// renderer and has no files, URLs, remote client, categories or upload service.
package notify

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const CallTimeout = 2 * time.Second

var ErrUnavailable = errors.New("native notification is unavailable")

type Notice struct {
	ID        string
	ContactID string
	Title     string
	Body      string
}

func (Notice) String() string               { return "[private WhatsUp native notification]" }
func (Notice) GoString() string             { return "[private WhatsUp native notification]" }
func (Notice) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func canonicalID(value string, nonzero bool) bool {
	if len(value) != 22 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != 16 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return false
	}
	if nonzero {
		var any byte
		for _, b := range decoded {
			any |= b
		}
		return any != 0
	}
	return true
}

func safeText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) && r != 0x200c && r != 0x200d {
			return false
		}
	}
	return true
}

type backend interface {
	start(context.Context) error
	send(context.Context, Notice) error
	close() error
	forget(string) error
}
type request struct {
	ctx      context.Context
	notice   Notice
	done     chan error
	forget   string
	approved bool
}
type Notifier struct{ *state }
type state struct {
	mu              sync.Mutex
	backend         backend
	started, closed bool
	available       bool
	closeErr        error
	life            context.Context
	cancel          context.CancelFunc
	requests        chan request
	done            chan struct{}
	sent            map[string]bool
	order           []string
	retired         map[string]bool
	retiredOrder    []string
}

func (Notifier) String() string               { return "[private WhatsUp native notifier]" }
func (Notifier) GoString() string             { return "[private WhatsUp native notifier]" }
func (Notifier) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func New(onActivate func(contactID string)) *Notifier {
	n := newNotifier(nil)
	n.backend = newBackend(func(id string) {
		n.dispatchActivation(id, onActivate)
	})
	return n
}

func (n *Notifier) dispatchActivation(id string, onActivate func(string)) {
	if !canonicalID(id, true) {
		return
	}
	// Serialize activation publication with Forget's local retirement.
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.closed && !n.retired[id] && onActivate != nil {
		onActivate(id)
	}
}
func newNotifier(native backend) *Notifier {
	return &Notifier{&state{backend: native, requests: make(chan request), done: make(chan struct{}), sent: map[string]bool{}, retired: map[string]bool{}}}
}

func (n *Notifier) Start(ctx context.Context) error {
	if n == nil || n.state == nil || ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	n.mu.Lock()
	if n.closed || n.backend == nil {
		n.mu.Unlock()
		return ErrUnavailable
	}
	if n.started {
		available := n.available
		n.mu.Unlock()
		if available {
			return nil
		}
		return ErrUnavailable
	}
	n.started = true
	n.life, n.cancel = context.WithCancel(ctx)
	ready := make(chan error, 1)
	go n.run(ready)
	n.mu.Unlock()
	bounded, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	select {
	case err := <-ready:
		if err != nil {
			return ErrUnavailable
		}
		return nil
	case <-bounded.Done():
		n.cancel()
		return ErrUnavailable
	}
}

func (n *Notifier) run(ready chan error) {
	defer func() {
		err := n.backend.close()
		n.mu.Lock()
		n.available = false
		if err != nil {
			n.closeErr = ErrUnavailable
		}
		n.mu.Unlock()
		close(n.done)
	}()
	if err := n.backend.start(n.life); err != nil {
		ready <- ErrUnavailable
		return
	}
	if n.life.Err() != nil {
		ready <- ErrUnavailable
		return
	}
	n.mu.Lock()
	n.available = true
	n.mu.Unlock()
	ready <- nil
	for {
		select {
		case <-n.life.Done():
			return
		case r := <-n.requests:
			if r.ctx.Err() != nil || n.life.Err() != nil {
				r.done <- ErrUnavailable
				continue
			}
			if r.forget != "" {
				r.done <- n.backend.forget(r.forget)
				continue
			}
			id := r.notice.ContactID + "/" + r.notice.ID
			n.mu.Lock()
			duplicate := n.sent[id]
			retired := n.retired[r.notice.ContactID]
			n.mu.Unlock()
			if retired {
				if !r.approved || n.backend.forget(r.notice.ContactID) != nil || r.ctx.Err() != nil {
					r.done <- ErrUnavailable
					continue
				}
				// Only the current service's approved-message boundary can revive
				// this exact ID after recovery. Never revive old issued proofs.
				n.mu.Lock()
				delete(n.retired, r.notice.ContactID)
				kept := n.retiredOrder[:0]
				for _, id := range n.retiredOrder {
					if id != r.notice.ContactID {
						kept = append(kept, id)
					}
				}
				n.retiredOrder = kept
				n.mu.Unlock()
			}
			if duplicate {
				r.done <- nil
				continue
			}
			err := n.backend.send(r.ctx, r.notice)
			if err != nil {
				r.done <- ErrUnavailable
				continue
			}
			n.mu.Lock()
			n.sent[id] = true
			n.order = append(n.order, id)
			if len(n.order) > 1024 {
				delete(n.sent, n.order[0])
				n.order = n.order[1:]
			}
			n.mu.Unlock()
			r.done <- nil
		}
	}
}

// ForgetContact is Go-only lifecycle cleanup. Retirement happens immediately;
// platform deletion is serialized after any in-flight notice and is bounded.
func (n *Notifier) ForgetContact(ctx context.Context, contactID string) error {
	if n == nil || n.state == nil || ctx == nil || ctx.Err() != nil || !canonicalID(contactID, true) {
		return ErrUnavailable
	}
	n.mu.Lock()
	if !n.retired[contactID] {
		n.retired[contactID] = true
		n.retiredOrder = append(n.retiredOrder, contactID)
		if len(n.retiredOrder) > maxNoticeReceipts {
			delete(n.retired, n.retiredOrder[0])
			n.retiredOrder = n.retiredOrder[1:]
		}
	}
	kept := n.order[:0]
	for _, id := range n.order {
		if strings.HasPrefix(id, contactID+"/") {
			delete(n.sent, id)
		} else {
			kept = append(kept, id)
		}
	}
	n.order = kept
	live := n.available && !n.closed
	life := n.life
	n.mu.Unlock()
	if !live {
		return ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	r := request{ctx: bounded, forget: contactID, done: make(chan error, 1)}
	select {
	case <-bounded.Done():
		return ErrUnavailable
	case <-life.Done():
		return ErrUnavailable
	case <-n.done:
		return ErrUnavailable
	case n.requests <- r:
	}
	select {
	case err := <-r.done:
		return err
	case <-bounded.Done():
		return ErrUnavailable
	case <-life.Done():
		return ErrUnavailable
	case <-n.done:
		return ErrUnavailable
	}
}

func (n *Notifier) Send(ctx context.Context, notice Notice) error { return n.send(ctx, notice, false) }

// SendApproved is used only inside desktop.WithIncomingNotification, which
// validates the current stored contact/message while serializing with Forget.
// That Go-only authority can restore notices for a contact recovered in-process.
func (n *Notifier) SendApproved(ctx context.Context, notice Notice) error {
	return n.send(ctx, notice, true)
}

func (n *Notifier) send(ctx context.Context, notice Notice, approved bool) error {
	if n == nil || n.state == nil || ctx == nil || ctx.Err() != nil || !canonicalID(notice.ID, false) || !canonicalID(notice.ContactID, true) || !safeText(notice.Title, 160) || !safeText(notice.Body, 1024) {
		return ErrUnavailable
	}
	n.mu.Lock()
	live := n.available && !n.closed
	life := n.life
	n.mu.Unlock()
	if !live {
		return ErrUnavailable
	}
	bounded, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	r := request{ctx: bounded, notice: notice, done: make(chan error, 1), approved: approved}
	select {
	case <-bounded.Done():
		return ErrUnavailable
	case <-life.Done():
		return ErrUnavailable
	case <-n.done:
		return ErrUnavailable
	case n.requests <- r:
	}
	select {
	case err := <-r.done:
		return err
	case <-bounded.Done():
		return ErrUnavailable
	case <-life.Done():
		return ErrUnavailable
	case <-n.done:
		return ErrUnavailable
	}
}

func (n *Notifier) Close() error {
	if n == nil || n.state == nil {
		return nil
	}
	n.mu.Lock()
	n.closed = true
	started := n.started
	if n.cancel != nil {
		n.cancel()
	}
	n.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-n.done:
		n.mu.Lock()
		err := n.closeErr
		n.mu.Unlock()
		return err
	case <-time.After(CallTimeout):
		return ErrUnavailable
	}
}
