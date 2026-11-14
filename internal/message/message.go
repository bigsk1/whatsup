// Package message contains neutral, backend-only persistence contracts shared
// by the chat domain and its encrypted repository. It has no network authority.
package message

import (
	"errors"
	"time"
)

const (
	Incoming = "incoming"
	Outgoing = "outgoing"
)

var (
	ErrCapacity = errors.New("contact message storage is full")
	ErrCorrupt  = errors.New("message storage could not be authenticated")
	ErrStore    = errors.New("message storage persistence failed")
	ErrNotFound = errors.New("message is not available")
	ErrConflict = errors.New("message ID refers to different content")
	ErrAck      = errors.New("acknowledgement does not refer to outgoing text")
	ErrClosed   = errors.New("message repository is closed")
)

type Message struct {
	ID        string    `json:"id"`
	Direction string    `json:"direction"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"createdAt"`
	Delivered bool      `json:"delivered"`
	Sequence  uint64    `json:"-"`
}

type Usage struct {
	UsedBytes  uint64 `json:"usedBytes"`
	QuotaBytes uint64 `json:"quotaBytes"`
	Full       bool   `json:"full"`
}

// Page carries an exclusive sequence cursor. Messages are in ascending order.
// NextCursor is the last scanned sequence while HasMore is true, even when a
// bounded scan found only locally hidden records. A zero input selects newest.
type Page struct {
	Messages   []Message `json:"messages"`
	NextCursor uint64    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
}

// PagedRepository is the optional event-driven conversation read contract.
// MarkRead requires a visible message ID and marks incoming messages through
// that message's sequence. It never changes delivery or acknowledgement state.
type PagedRepository interface {
	ReadPage(before uint64, limit int) (Page, error)
	UnreadCount() (uint64, error)
	MarkRead(throughMessageID string) error
}

// Repository is scoped to one approved contact by its caller. Pending pairing
// must never receive a repository capability. Queue and receive are durable on
// success; only Acknowledge may remove an outgoing entry from the outbox.
type Repository interface {
	Queue(Message) error
	Receive(Message) (duplicate bool, err error)
	Acknowledge(id string) error
	PendingIDs() ([]string, error)
	Get(id string) (Message, error)
	Page(beforeSequence uint64, limit int) ([]Message, error)
	Usage() (Usage, error)
	DeleteLocal(id string) error
	ClearHistory() error
}
