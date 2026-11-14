package chat

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"sync"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/pairing"
)

const (
	MaxMessages        = 256
	MaxStoredTextBytes = 256 << 10
	MaxSnapshotBytes   = 2 << 20 // below the encrypted vault's 4 MiB safety limit
	RetryMin           = time.Second
	RetryMax           = 5 * time.Minute
	Incoming           = "incoming"
	Outgoing           = "outgoing"
)

var (
	ErrContact  = errors.New("approved conversation contact required")
	ErrCapacity = errors.New("local conversation capacity reached")
	ErrStore    = errors.New("conversation persistence failed; reopen before retrying")
	ErrCorrupt  = errors.New("conversation state is invalid or belongs to another identity")
	ErrConflict = errors.New("message ID already refers to different content")
	ErrAck      = errors.New("acknowledgement does not refer to an outgoing message")
	ErrRetry    = errors.New("invalid retry delay")
)

// Store must encrypt state with OS-protected credentials and make Save durable
// before returning success. Use a fresh storage.ApplicationState.ChatStore for
// each Conversation. It preserves identity and other contacts in one envelope
// and rejects reused or stale writers. Never pass the application's raw Vault
// here. There is no plaintext filesystem fallback here.
type Store interface {
	Load() ([]byte, error)
	Save([]byte) error
}

type Options struct {
	Now func() time.Time
	// Jitter is injectable for scheduling tests. Returning a delay outside the
	// supplied inclusive bounds fails closed. Production defaults to crypto/rand.
	Jitter func(minimum, maximum time.Duration) (time.Duration, error)
}

type Message struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	Delivered bool      `json:"delivered"`
}

type Attempt struct {
	ID          string    `json:"id"`
	Count       uint32    `json:"count"`
	NextAttempt time.Time `json:"nextAttempt"`
}

type snapshot struct {
	Version  int                `json:"version"`
	Local    identity.PublicKey `json:"local"`
	Peer     identity.PublicKey `json:"peer"`
	Messages []Message          `json:"messages"`
	Outbox   []Attempt          `json:"outbox"`
}

// Conversation has no bridge binding, networking, or pending-pairing API. Its
// constructor requires a locally approved contact. The caller remains responsible
// for the additional mutual-authentication/durable-activation requirement.
type Conversation struct {
	mu      sync.Mutex
	store   Store
	contact *pairing.Contact
	now     func() time.Time
	jitter  func(time.Duration, time.Duration) (time.Duration, error)
	state   snapshot
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
	conversation := &Conversation{store: store, contact: contact, now: options.Now, jitter: options.Jitter}
	data, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		state := snapshot{Version: ProtocolVersion, Local: contact.Local(), Peer: contact.Peer()}
		if err := conversation.commit(state); err != nil {
			return nil, err
		}
		return conversation, nil
	}
	if err != nil {
		return nil, errors.Join(ErrStore, err)
	}
	if len(data) == 0 || len(data) > MaxSnapshotBytes {
		return nil, ErrCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&conversation.state); err != nil {
		return nil, ErrCorrupt
	}
	canonical, err := json.Marshal(conversation.state)
	if err != nil || !bytes.Equal(data, canonical) || !conversation.state.Local.Equal(contact.Local()) || !conversation.state.Peer.Equal(contact.Peer()) || validateSnapshot(conversation.state) != nil {
		return nil, ErrCorrupt
	}
	return conversation, nil
}

// Queue persists local text before reporting success, even while the peer is
// offline. It grants no pending identity access and never transmits by itself.
func (c *Conversation) Queue(text string) (message Message, err error) {
	err = c.withPeer(c.contact.Peer(), func() error {
		if err := validateText(text); err != nil {
			return err
		}
		if len(c.state.Messages) >= MaxMessages || storedBytes(c.state)+len(text) > MaxStoredTextBytes {
			return ErrCapacity
		}
		var id string
		for attempts := 0; attempts < 3; attempts++ {
			var bytes [16]byte
			if _, err := rand.Read(bytes[:]); err != nil {
				return err
			}
			id = base64.RawURLEncoding.EncodeToString(bytes[:])
			if findMessage(c.state.Messages, id) < 0 {
				break
			}
			id = ""
		}
		if id == "" {
			return ErrConflict
		}
		queued := Message{ID: id, Direction: Outgoing, Text: text, CreatedAt: c.now().UTC()}
		next := clone(c.state)
		next.Messages = append(next.Messages, queued)
		next.Outbox = append(next.Outbox, Attempt{ID: id, NextAttempt: queued.CreatedAt})
		if err := c.commit(next); err != nil {
			return err
		}
		message = queued
		return nil
	})
	return message, err
}

// NextAttempt returns one due frame only for the pinned authenticated session
// peer. Persisting its next retry before handing it to a sender bounds retries
// across crashes. Successful socket writes never remove messages from the outbox.
func (c *Conversation) NextAttempt(authenticatedPeer identity.PublicKey) (frame Frame, due bool, err error) {
	err = c.withPeer(authenticatedPeer, func() error {
		now := c.now().UTC()
		for index, attempt := range c.state.Outbox {
			if now.Before(attempt.NextAttempt) {
				continue
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
			next := clone(c.state)
			if next.Outbox[index].Count < ^uint32(0) {
				next.Outbox[index].Count++
			}
			next.Outbox[index].NextAttempt = now.Add(delay)
			message := c.state.Messages[findMessage(c.state.Messages, attempt.ID)]
			if err := c.commit(next); err != nil {
				return err
			}
			frame = Frame{Version: ProtocolVersion, Type: MessageFrame, ID: message.ID, Text: message.Text}
			due = true
			return nil
		}
		return nil
	})
	return frame, due, err
}

// Receive returns an acknowledgement only after new incoming text and its
// deduplication ID have been saved durably. Lost acknowledgements can be retried
// after restart without inserting another message. IDs are never silently evicted;
// reaching the bounded history capacity rejects new messages explicitly.
func (c *Conversation) Receive(authenticatedPeer identity.PublicKey, frame Frame) (ack Frame, err error) {
	err = c.withPeer(authenticatedPeer, func() error {
		if frame.Type != MessageFrame || frame.Validate() != nil {
			return ErrFrame
		}
		if index := findMessage(c.state.Messages, frame.ID); index >= 0 {
			message := c.state.Messages[index]
			if message.Direction != Incoming || message.Text != frame.Text {
				return ErrConflict
			}
			ack = Frame{Version: ProtocolVersion, Type: AckFrame, ID: frame.ID}
			return nil
		}
		if len(c.state.Messages) >= MaxMessages || storedBytes(c.state)+len(frame.Text) > MaxStoredTextBytes {
			return ErrCapacity
		}
		next := clone(c.state)
		next.Messages = append(next.Messages, Message{ID: frame.ID, Direction: Incoming, Text: frame.Text, CreatedAt: c.now().UTC(), Delivered: true})
		if err := c.commit(next); err != nil {
			return err
		}
		ack = Frame{Version: ProtocolVersion, Type: AckFrame, ID: frame.ID}
		return nil
	})
	return ack, err
}

// Acknowledge removes only the acknowledged outgoing queue entry, while keeping
// its delivered history. The session must authenticate the peer; a matching
// claimed ID alone is never sufficient. Repeated authenticated acks are idempotent.
func (c *Conversation) Acknowledge(authenticatedPeer identity.PublicKey, frame Frame) error {
	return c.withPeer(authenticatedPeer, func() error {
		if frame.Type != AckFrame || frame.Validate() != nil {
			return ErrFrame
		}
		index := findMessage(c.state.Messages, frame.ID)
		if index < 0 || c.state.Messages[index].Direction != Outgoing {
			return ErrAck
		}
		if c.state.Messages[index].Delivered {
			return nil
		}
		next := clone(c.state)
		next.Messages[index].Delivered = true
		for pending, attempt := range next.Outbox {
			if attempt.ID == frame.ID {
				next.Outbox = append(next.Outbox[:pending], next.Outbox[pending+1:]...)
				break
			}
		}
		return c.commit(next)
	})
}

// Messages and Outbox are backend-only copies, not an unguarded bridge API.
func (c *Conversation) Messages() []Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Message(nil), c.state.Messages...)
}

func (c *Conversation) Outbox() []Attempt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Attempt(nil), c.state.Outbox...)
}

// HasCapacity reports whether at least one more text byte can be queued. The
// actual text and remaining byte budget are still checked atomically by Queue.
func (c *Conversation) HasCapacity() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.failed && len(c.state.Messages) < MaxMessages && storedBytes(c.state) < MaxStoredTextBytes
}

// ResetRetries makes the durable outbox immediately eligible after a freshly
// authenticated connection. A new session must not inherit a five-minute wait
// accumulated while the peer was unavailable. Nothing is acknowledged/deleted.
func (c *Conversation) ResetRetries(authenticatedPeer identity.PublicKey) error {
	return c.withPeer(authenticatedPeer, func() error {
		if len(c.state.Outbox) == 0 {
			return nil
		}
		next := clone(c.state)
		for index := range next.Outbox {
			next.Outbox[index].Count = 0
			next.Outbox[index].NextAttempt = c.now().UTC()
		}
		return c.commit(next)
	})
}

func (c *Conversation) withPeer(peer identity.PublicKey, operation func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return ErrStore
	}
	return c.contact.WithAuthenticatedPeer(peer, operation)
}

func (c *Conversation) commit(next snapshot) error {
	if err := validateSnapshot(next); err != nil {
		return err
	}
	data, err := json.Marshal(next)
	if err != nil || len(data) > MaxSnapshotBytes {
		return ErrCapacity
	}
	if err := c.store.Save(data); err != nil {
		// The store may have renamed its file before reporting a directory-sync
		// failure. Keep memory unchanged and require reopening to reconcile any
		// durable change; continuing could otherwise overwrite uncertain state.
		c.failed = true
		return errors.Join(ErrStore, err)
	}
	c.state = next
	return nil
}

func validateSnapshot(state snapshot) error {
	if state.Version != ProtocolVersion || !state.Local.Valid() || !state.Peer.Valid() || state.Local.Equal(state.Peer) || len(state.Messages) > MaxMessages || len(state.Outbox) > MaxMessages || storedBytes(state) > MaxStoredTextBytes {
		return ErrCorrupt
	}
	seen := make(map[string]Message, len(state.Messages))
	for _, message := range state.Messages {
		if !validID(message.ID) || validateText(message.Text) != nil || message.CreatedAt.IsZero() || (message.Direction != Incoming && message.Direction != Outgoing) || (message.Direction == Incoming && !message.Delivered) {
			return ErrCorrupt
		}
		if _, duplicate := seen[message.ID]; duplicate {
			return ErrCorrupt
		}
		seen[message.ID] = message
	}
	pending := make(map[string]bool, len(state.Outbox))
	for _, attempt := range state.Outbox {
		message, exists := seen[attempt.ID]
		if !exists || message.Direction != Outgoing || message.Delivered || pending[attempt.ID] || attempt.NextAttempt.IsZero() {
			return ErrCorrupt
		}
		pending[attempt.ID] = true
	}
	for _, message := range state.Messages {
		if message.Direction == Outgoing && message.Delivered == pending[message.ID] {
			return ErrCorrupt
		}
	}
	return nil
}

func clone(state snapshot) snapshot {
	state.Messages = append([]Message(nil), state.Messages...)
	state.Outbox = append([]Attempt(nil), state.Outbox...)
	return state
}

func storedBytes(state snapshot) int {
	bytes := 0
	for _, message := range state.Messages {
		bytes += len(message.Text)
	}
	return bytes
}

func findMessage(messages []Message, id string) int {
	for index, message := range messages {
		if message.ID == id {
			return index
		}
	}
	return -1
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
