package messaging

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"

	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
)

const (
	controlPacket         byte = 7
	removedControl        byte = 1
	removedReceiptControl byte = 2
	maxControlBody             = 4096
	RemovalTimeout             = 2 * time.Second
)

var (
	errPeerRemoved = errors.New("authenticated peer removed this contact")
	errRemovalSent = errors.New("contact removal notice completed")
)

// Only fixed reviewed relay control types16–19 are accepted. Unknown controls
// are rejected before reading their body; nothing is forwarded to JS.
type controlFrame struct {
	kind byte
	id   string
	body []byte
}

func validControlBody(kind byte, length int) bool {
	switch kind {
	case removedControl, removedReceiptControl:
		return length == 0
	case routeReadyControl:
		return length == 56
	case relayContributionControl:
		return length > 0 && length <= maxControlBody
	case relayCommitControl, relayReceiptControl:
		return length > 0 && length <= 512
	}
	return false
}

func validControlID(id string) bool {
	if len(id) != 22 || id == "AAAAAAAAAAAAAAAAAAAAAA" {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(id)
	return err == nil && len(raw) == 16 && base64.RawURLEncoding.EncodeToString(raw) == id
}

func encodeControl(f controlFrame) ([]byte, error) {
	if !validControlBody(f.kind, len(f.body)) || !validControlID(f.id) {
		return nil, errProtocol
	}
	raw, _ := base64.RawURLEncoding.Strict().DecodeString(f.id)
	wire := []byte{controlPacket, f.kind}
	wire = append(wire, raw...)
	wire = binary.BigEndian.AppendUint16(wire, uint16(len(f.body)))
	wire = append(wire, f.body...)
	return wire, nil
}

func readControl(reader io.Reader) (controlFrame, error) {
	var header [19]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return controlFrame{}, errProtocol
	}
	length := binary.BigEndian.Uint16(header[17:])
	f := controlFrame{kind: header[0], id: base64.RawURLEncoding.EncodeToString(header[1:17])}
	if length > maxControlBody || !validControlBody(f.kind, int(length)) || !validControlID(f.id) {
		return controlFrame{}, errProtocol
	}
	f.body = make([]byte, int(length))
	if _, err := io.ReadFull(reader, f.body); err != nil {
		return controlFrame{}, errProtocol
	}
	return f, nil
}

type controlRequest struct {
	ctx   context.Context
	frame controlFrame
	done  chan bool
	once  sync.Once
}

func (r *controlRequest) finish(receipt bool) { r.once.Do(func() { r.done <- receipt }) }

// A channel belongs to one authenticated session generation. Reconnection never
// inherits a queued control or a receipt from an older TLS transcript.
type controlSession struct {
	requests chan *controlRequest
	closed   chan struct{}
	session  *sessionauth.Session
	route    approvedRoute
	proof    *storage.RouteSessionProof
}

// NotifyRemoved sends only through an already connected, compatible session.
// It never creates a manager, dials, fetches a relay map, or delays local Forget
// beyond the caller's deadline and the fixed two-second ceiling. A true result
// means the peer reported durable receipt; local Forget proceeds either way.
func (r *Registry) NotifyRemoved(ctx context.Context, id string) (bool, error) {
	if r == nil || r.registryState == nil || ctx == nil || ctx.Err() != nil {
		return false, ErrUnavailable
	}
	r.mu.Lock()
	m := r.managers[id]
	unavailable := r.closed || r.retired[id]
	r.mu.Unlock()
	if unavailable || m == nil {
		return false, nil
	}
	return m.notifyRemoved(ctx, id)
}

func (m *Manager) notifyRemoved(ctx context.Context, id string) (bool, error) {
	work, cancel := context.WithTimeout(ctx, RemovalTimeout)
	defer cancel()
	m.mu.Lock()
	c := m.controls
	if m.closed || m.refreshLocked() != nil || m.record.ID != id || m.removing || m.peerRemoved || !m.sessionActive || c == nil || !c.session.Capabilities().Control {
		m.mu.Unlock()
		return false, nil
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		m.mu.Unlock()
		return false, ErrUnavailable
	}
	if token == [16]byte{} {
		m.mu.Unlock()
		return false, ErrUnavailable
	}
	request := &controlRequest{ctx: work, frame: controlFrame{kind: removedControl, id: base64.RawURLEncoding.EncodeToString(token[:])}, done: make(chan bool, 1)}
	// Block new local messages for this generation once Forget has begun.
	m.removing = true
	m.bumpLocked()
	select {
	case c.requests <- request:
	default:
		m.mu.Unlock()
		return false, ErrUnavailable
	}
	m.mu.Unlock()
	select {
	case receipt := <-request.done:
		return receipt, nil
	case <-c.closed:
		select {
		case receipt := <-request.done:
			return receipt, nil
		default:
			return false, nil
		}
	case <-work.Done():
		return false, nil
	}
}

func (m *Manager) refreshRemovalLocked() error {
	if m.conversation == nil {
		return nil
	}
	id, err := m.application.PeerRemoval(m.record)
	if err != nil {
		m.failLocked()
		return ErrUnavailable
	}
	if id != "" {
		if !m.peerRemoved || m.state != "peer-removed" {
			m.peerRemoved = true
			m.state = "peer-removed"
			m.bumpLocked()
		}
	}
	return nil
}
