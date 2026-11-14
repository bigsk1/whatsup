package chat

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"math/big"
	"sync"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/message"
	"whatsup/internal/pairing"
)

const (
	MaxPageMessages  = 100
	MaxPageTextBytes = 1 << 20
	RetryMin         = time.Second
	RetryMax         = 5 * time.Minute
	Incoming         = message.Incoming
	Outgoing         = message.Outgoing
)

var (
	ErrContact  = errors.New("approved conversation contact required")
	ErrCapacity = message.ErrCapacity
	ErrStore    = message.ErrStore
	ErrCorrupt  = message.ErrCorrupt
	ErrConflict = message.ErrConflict
	ErrAck      = message.ErrAck
	ErrRetry    = errors.New("invalid retry delay")
	ErrPage     = errors.New("invalid conversation page request")
)

// Store is a contact-scoped encrypted record repository. The application must
// create it only for this exact approved contact; never supply a raw database or
// a snapshot writer. No chat operation serializes or rewrites whole history.
type Store = message.Repository

type Message = message.Message

type Options struct {
	Now func() time.Time
	// Jitter is injectable for scheduling tests. Returning a delay outside the
	// supplied inclusive bounds fails closed. Production defaults to crypto/rand.
	Jitter func(minimum, maximum time.Duration) (time.Duration, error)
}

type Attempt struct {
	ID          string    `json:"id"`
	Count       uint32    `json:"count"`
	NextAttempt time.Time `json:"nextAttempt"`
}

// Conversation has no bridge binding, networking, or pending-pairing API. Its
// constructor requires a locally approved contact. The caller remains responsible
// for the additional mutual-authentication/durable-activation requirement.
// Only retry deadlines and ordering live here; durable state belongs to Store.
type Conversation struct {
	mu      sync.Mutex
	store   Store
	contact *pairing.Contact
	now     func() time.Time
	jitter  func(time.Duration, time.Duration) (time.Duration, error)
	pending []string
	retries map[string]Attempt
	failed  bool
}

func Open(store Store, contact *pairing.Contact, options Options) (*Conversation, error) {
	if store == nil || contact == nil || !contact.Local().Valid() || !contact.Peer().Valid() || contact.Local().Equal(contact.Peer()) {
		return nil, ErrContact
	}
	if err := contact.AuthorizeAuthenticatedPeer(contact.Peer()); err != nil {
		return nil, ErrContact
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Jitter == nil {
		options.Jitter = randomDelay
	}
	c := &Conversation{store: store, contact: contact, now: options.Now, jitter: options.Jitter, retries: make(map[string]Attempt)}
	err := c.withPeer(contact.Peer(), func() error {
		if scoped, ok := store.(interface{ Claim() error }); ok {
			if err := scoped.Claim(); err != nil {
				return err
			}
		}
		ids, err := store.PendingIDs()
		if err != nil {
			return c.repositoryError(err)
		}
		now := c.now()
		for _, id := range ids {
			if !validID(id) {
				return ErrCorrupt
			}
			if _, duplicate := c.retries[id]; duplicate {
				return ErrCorrupt
			}
			c.pending = append(c.pending, id)
			c.retries[id] = Attempt{ID: id, NextAttempt: now}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Queue commits the message and its durable outbox membership atomically before
// returning its stable ID. Scheduling is in memory and does not touch the vault.
func (c *Conversation) Queue(text string) (queued Message, err error) {
	err = c.withPeer(c.peer(), func() error {
		if err := validateText(text); err != nil {
			return err
		}
		now := c.now()
		if now.IsZero() {
			return ErrCorrupt
		}
		for attempt := 0; attempt < 3; attempt++ {
			var id [16]byte
			if _, err := rand.Read(id[:]); err != nil {
				return err
			}
			candidate := Message{ID: base64.RawURLEncoding.EncodeToString(id[:]), Direction: Outgoing, Text: text, CreatedAt: now.UTC()}
			if err := c.store.Queue(candidate); err != nil {
				if errors.Is(err, ErrConflict) {
					continue
				}
				return c.repositoryError(err)
			}
			c.pending = append(c.pending, candidate.ID)
			c.retries[candidate.ID] = Attempt{ID: candidate.ID, NextAttempt: now}
			queued = candidate
			return nil
		}
		return ErrConflict
	})
	return queued, err
}

// NextAttempt reads only the next due durable record. A socket write never
// acknowledges a message. Backoff changes remain in memory; reopening rebuilds
// immediately eligible attempts from authenticated encrypted outbox records.
func (c *Conversation) NextAttempt(authenticatedPeer identity.PublicKey) (frame Frame, due bool, err error) {
	err = c.withPeer(authenticatedPeer, func() error {
		now := c.now()
		for _, id := range c.pending {
			attempt := c.retries[id]
			if now.Before(attempt.NextAttempt) {
				continue
			}
			stored, err := c.store.Get(id)
			if err != nil {
				if errors.Is(err, message.ErrNotFound) {
					c.failed = true
					return ErrCorrupt
				}
				return c.repositoryError(err)
			}
			if stored.ID != id || validateStored(stored) != nil || stored.Direction != Outgoing || stored.Delivered {
				c.failed = true
				return ErrCorrupt
			}
			ceiling := retryCeiling(attempt.Count)
			floor := ceiling / 2
			if floor < RetryMin {
				floor = RetryMin
			}
			delay, err := c.jitter(floor, ceiling)
			if err != nil {
				return err
			}
			if delay < floor || delay > ceiling {
				return ErrRetry
			}
			if attempt.Count < ^uint32(0) {
				attempt.Count++
			}
			attempt.NextAttempt = now.Add(delay)
			c.retries[id] = attempt
			frame = Frame{Version: ProtocolVersion, Type: MessageFrame, ID: id, Text: stored.Text}
			due = true
			return nil
		}
		return nil
	})
	return frame, due, err
}

// Receive returns an acknowledgement only after the incoming record and its
// duplicate-check state are committed. A duplicate is checked before disk quota;
// a lost ACK cannot turn a saved message into another history entry.
func (c *Conversation) Receive(authenticatedPeer identity.PublicKey, frame Frame) (ack Frame, err error) {
	err = c.withPeer(authenticatedPeer, func() error {
		if frame.Type != MessageFrame || frame.Validate() != nil {
			return ErrFrame
		}
		if _, err := c.store.Receive(Message{ID: frame.ID, Direction: Incoming, Text: frame.Text, CreatedAt: c.now().UTC(), Delivered: true}); err != nil {
			return c.repositoryError(err)
		}
		ack = Frame{Version: ProtocolVersion, Type: AckFrame, ID: frame.ID}
		return nil
	})
	return ack, err
}

// Acknowledge atomically marks the record delivered and removes its encrypted
// outbox membership. Only after that commit does memory forget the retry entry.
func (c *Conversation) Acknowledge(authenticatedPeer identity.PublicKey, frame Frame) error {
	return c.withPeer(authenticatedPeer, func() error {
		if frame.Type != AckFrame || frame.Validate() != nil {
			return ErrFrame
		}
		if err := c.store.Acknowledge(frame.ID); err != nil {
			return c.repositoryError(err)
		}
		delete(c.retries, frame.ID)
		for index, id := range c.pending {
			if id == frame.ID {
				c.pending = append(c.pending[:index], c.pending[index+1:]...)
				break
			}
		}
		return nil
	})
}

// Messages is a bounded compatibility snapshot for the current single-contact
// UI. New callers should use Page so read failures remain explicit.
func (c *Conversation) Messages() []Message {
	messages, _ := c.Page(0, MaxPageMessages)
	return messages
}

func (c *Conversation) Page(beforeSequence uint64, limit int) (messages []Message, err error) {
	if limit < 1 || limit > MaxPageMessages {
		return nil, ErrPage
	}
	err = c.withPeer(c.peer(), func() error {
		stored, err := c.store.Page(beforeSequence, limit)
		if err != nil {
			return c.repositoryError(err)
		}
		if len(stored) > limit {
			c.failed = true
			return ErrCorrupt
		}
		seen := make(map[string]bool, len(stored))
		var previous uint64
		total := 0
		for _, item := range stored {
			if validateStored(item) != nil || seen[item.ID] || item.Sequence <= previous || beforeSequence != 0 && item.Sequence >= beforeSequence {
				c.failed = true
				return ErrCorrupt
			}
			seen[item.ID] = true
			previous = item.Sequence
			total += len(item.Text)
		}
		start := 0
		for total > MaxPageTextBytes && start < len(stored) {
			total -= len(stored[start].Text)
			start++
		}
		messages = append([]Message(nil), stored[start:]...)
		return nil
	})
	return messages, err
}

func (c *Conversation) Usage() (usage message.Usage, err error) {
	err = c.withPeer(c.peer(), func() error {
		var err error
		usage, err = c.store.Usage()
		return c.repositoryError(err)
	})
	return usage, err
}

func (c *Conversation) HasCapacity() bool {
	usage, err := c.Usage()
	return err == nil && !usage.Full
}

func (c *Conversation) DeleteLocal(id string) error {
	if !validID(id) {
		return ErrFrame
	}
	return c.withPeer(c.peer(), func() error { return c.repositoryError(c.store.DeleteLocal(id)) })
}

// ClearHistory removes local visibility without revoking the contact or silently
// deleting unacknowledged outgoing work. The repository retains pending bodies
// and deduplication evidence required for safe retries.
func (c *Conversation) ClearHistory() error {
	return c.withPeer(c.peer(), func() error { return c.repositoryError(c.store.ClearHistory()) })
}

func (c *Conversation) Outbox() (attempts []Attempt) {
	_ = c.withPeer(c.peer(), func() error {
		for _, id := range c.pending {
			attempts = append(attempts, c.retries[id])
		}
		return nil
	})
	return attempts
}

// ResetRetries wakes only this approved conversation after fresh authentication.
// It writes no retry timestamp or retry count to persistent storage.
func (c *Conversation) ResetRetries(authenticatedPeer identity.PublicKey) error {
	return c.withPeer(authenticatedPeer, func() error {
		now := c.now()
		for _, id := range c.pending {
			c.retries[id] = Attempt{ID: id, NextAttempt: now}
		}
		return nil
	})
}

func (c *Conversation) peer() identity.PublicKey {
	if c == nil || c.contact == nil {
		return identity.PublicKey{}
	}
	return c.contact.Peer()
}

func (c *Conversation) withPeer(peer identity.PublicKey, operation func() error) error {
	if c == nil || c.contact == nil || c.store == nil || operation == nil {
		return ErrContact
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return ErrStore
	}
	return c.contact.WithAuthenticatedPeer(peer, operation)
}

func (c *Conversation) repositoryError(err error) error {
	if err == nil || errors.Is(err, ErrCapacity) || errors.Is(err, ErrConflict) || errors.Is(err, ErrAck) || errors.Is(err, message.ErrNotFound) {
		return err
	}
	c.failed = true
	if errors.Is(err, ErrCorrupt) {
		return errors.Join(ErrCorrupt, err)
	}
	return errors.Join(ErrStore, err)
}

func validateStored(item Message) error {
	if !validID(item.ID) || validateText(item.Text) != nil || item.CreatedAt.IsZero() || item.Sequence == 0 ||
		(item.Direction != Incoming && item.Direction != Outgoing) || item.Direction == Incoming && !item.Delivered {
		return ErrCorrupt
	}
	return nil
}

func retryCeiling(count uint32) time.Duration {
	if count >= 8 {
		return RetryMax
	}
	ceiling := RetryMin * time.Duration(uint64(1)<<(count+1))
	if ceiling > RetryMax {
		return RetryMax
	}
	return ceiling
}

func randomDelay(minimum, maximum time.Duration) (time.Duration, error) {
	span := big.NewInt(int64(maximum-minimum) + 1)
	value, err := rand.Int(rand.Reader, span)
	if err != nil {
		return 0, err
	}
	return minimum + time.Duration(value.Int64()), nil
}
