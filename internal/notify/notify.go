// Package notify owns narrow native message notices. It is never bound to the
// renderer and has no files, URLs, remote client, categories or upload service.
package notify

import (
	"context"
	"encoding/base64"
	"errors"
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
}
type request struct {
	ctx    context.Context
	notice Notice
	done   chan error
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
}

func (Notifier) String() string               { return "[private WhatsUp native notifier]" }
func (Notifier) GoString() string             { return "[private WhatsUp native notifier]" }
func (Notifier) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func New(onActivate func(contactID string)) *Notifier { return newNotifier(newBackend(onActivate)) }
func newNotifier(native backend) *Notifier {
	return &Notifier{&state{backend: native, requests: make(chan request), done: make(chan struct{}), sent: map[string]bool{}}}
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
			id := r.notice.ContactID + "/" + r.notice.ID
			n.mu.Lock()
			duplicate := n.sent[id]
			n.mu.Unlock()
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

func (n *Notifier) Send(ctx context.Context, notice Notice) error {
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
	r := request{ctx: bounded, notice: notice, done: make(chan error, 1)}
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
