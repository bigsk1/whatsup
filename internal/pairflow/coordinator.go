// Package pairflow coordinates reserved pairing and explicit user decisions.
// It owns supplied connections, never listens/dials, and creates only inert
// encrypted pending records. There is no contact or network activation API.
package pairflow

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"sync"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/pairing"
	"whatsup/internal/pairing/channel"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

var (
	ErrUnavailable = errors.New("pairing workflow is unavailable")
	ErrRequest     = errors.New("pairing request is no longer available")
	ErrStore       = errors.New("pending pairing storage requires attention")
	ErrProtocol    = errors.New("pairing setup did not complete")
)

// Snapshot is the entire public pairing DTO. No names, links, node keys, PSKs,
// raw errors or records cross the bridge. RequestID is an ephemeral UI token.
type Snapshot struct {
	State            string `json:"state"`
	RequestID        string `json:"requestID"`
	LocalFingerprint string `json:"localFingerprint"`
	PeerFingerprint  string `json:"peerFingerprint"`
	ExpiresAt        string `json:"expiresAt"`
}

type Store interface {
	SavePendingContact(storage.PendingRecord) error
	PendingContacts() ([]storage.PendingRecord, error)
	RemovePendingContact(string) error
}

// Copies share closure and request ownership; value formatting never traverses
// locks, identities, stores or pending credentials.
type Coordinator struct{ *coordinatorState }

type coordinatorState struct {
	mu      sync.Mutex
	local   *identity.Identity
	store   Store
	current *attempt
	status  Snapshot
	closed  bool
	runs    sync.WaitGroup
	actions sync.WaitGroup
}

type attempt struct {
	mu           sync.Mutex
	closeOnce    sync.Once
	closeErr     error
	raw          net.Conn
	session      *channel.Session
	manager      *pairing.Manager
	invite       pairing.Invite
	cancel       context.CancelFunc
	ctx          context.Context
	done         chan struct{}
	decisions    chan channel.Decision
	id           string
	peer         identity.PublicKey
	explicitDeny bool // protected by Coordinator.mu
	// The single run owns these until done closes. Deny waits for done before
	// removing the record, so a late persistence callback cannot resurrect it.
	pendingID string
	saved     bool
}

func (Coordinator) String() string               { return "[private WhatsUp pairing workflow]" }
func (Coordinator) GoString() string             { return "[private WhatsUp pairing workflow]" }
func (Coordinator) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func New(local *identity.Identity, store Store) (*Coordinator, error) {
	if local == nil || store == nil {
		return nil, ErrUnavailable
	}
	c := &Coordinator{&coordinatorState{local: local, store: store, status: Snapshot{State: "idle"}}}
	records, err := store.PendingContacts()
	if err != nil || c.restorePending(records) != nil {
		return nil, ErrStore
	}
	return c, nil
}

func requestID() (string, error) {
	var token [16]byte
	for token == [16]byte{} {
		if _, err := rand.Read(token[:]); err != nil {
			return "", ErrUnavailable
		}
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

// restorePending is called before publication, or with the coordinator mutex
// held after a finished explicit discard. It never revives an invitation or
// approval. Restored pending state is a nonmodal discardable notice in the UI.
func (c *Coordinator) restorePending(records []storage.PendingRecord) error {
	for _, record := range records {
		if storage.ValidatePendingRecord(record) != nil || !record.Local.Equal(c.local.PublicKey()) {
			return ErrStore
		}
	}
	if len(records) == 0 {
		return nil
	}
	id, err := requestID()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &attempt{id: id, peer: records[0].Peer, ctx: ctx, cancel: cancel,
		done: make(chan struct{}), pendingID: records[0].ID, saved: true}
	close(a.done)
	c.current = a
	c.status = Snapshot{State: "pending", RequestID: id,
		LocalFingerprint: c.local.PublicKey().Fingerprint(), PeerFingerprint: a.peer.Fingerprint()}
	return nil
}

func (c *Coordinator) Snapshot() Snapshot {
	if c == nil || c.coordinatorState == nil {
		return Snapshot{State: "unavailable"}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.State == "" {
		return Snapshot{State: "unavailable"}
	}
	return c.status
}

// Accept takes ownership of raw and manager, including on failure. Only the
// reservation callback can select a candidate, before either UI sees a key.
// The retirement hook may close its underlying listener, never call back here.
func (c *Coordinator) Accept(ctx context.Context, raw net.Conn, invite pairing.Invite, manager *pairing.Manager) error {
	return c.run(ctx, raw, invite, manager, channel.Inviter)
}

// Join uses the exact owner in the parsed invitation. The owner must acknowledge
// bearer reservation before this endpoint publishes a confirmation request.
func (c *Coordinator) Join(ctx context.Context, raw net.Conn, invite pairing.Invite) error {
	return c.run(ctx, raw, invite, nil, channel.Joiner)
}

func (c *Coordinator) begin(ctx context.Context, raw net.Conn, invite pairing.Invite, manager *pairing.Manager, role channel.Role) (*attempt, error) {
	reject := func() (*attempt, error) {
		if raw != nil {
			_ = raw.Close()
		}
		// A second candidate on an invitation's listener must not retire the
		// manager already owned by the first reserved attempt.
		owned := false
		if c != nil && c.coordinatorState != nil {
			c.mu.Lock()
			owned = c.current != nil && c.current.manager == manager
			c.mu.Unlock()
		}
		if manager != nil && !owned {
			_ = manager.Close()
		}
		return nil, ErrUnavailable
	}
	if c == nil || c.coordinatorState == nil || c.local == nil || c.store == nil || ctx == nil || raw == nil || !invite.Endpoint().Valid() || invite.ChannelID() == [16]byte{} || !time.Now().Before(invite.ExpiresAt()) || (role == channel.Inviter && (manager == nil || !invite.Owner().Equal(c.local.PublicKey()))) || (role == channel.Joiner && invite.Owner().Equal(c.local.PublicKey())) {
		return reject()
	}
	id, err := requestID()
	if err != nil {
		return reject()
	}
	c.mu.Lock()
	if c.closed || c.current != nil {
		c.mu.Unlock()
		return reject()
	}
	work, cancel := context.WithDeadline(ctx, invite.ExpiresAt())
	a := &attempt{raw: raw, manager: manager, invite: invite, id: id, ctx: work,
		cancel: cancel, done: make(chan struct{}), decisions: make(chan channel.Decision, 1)}
	c.current, c.status = a, Snapshot{State: "connecting"}
	c.runs.Add(1)
	c.mu.Unlock()
	return a, nil
}

func (a *attempt) setSession(session *channel.Session) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx.Err() != nil {
		_ = session.Close()
		return ErrProtocol
	}
	a.session = session
	return nil
}

func (a *attempt) closeOwned() {
	a.closeOnce.Do(func() {
		a.cancel()
		a.mu.Lock()
		session := a.session
		a.mu.Unlock()
		if session != nil {
			_ = session.Close()
		}
		if a.raw != nil {
			_ = a.raw.Close()
		}
		if a.manager != nil && a.manager.Close() != nil {
			a.closeErr = ErrProtocol
		}
	})
}

func (c *Coordinator) setState(a *attempt, state string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.current != a || a.explicitDeny || a.ctx.Err() != nil || !time.Now().Before(a.invite.ExpiresAt()) {
		return false
	}
	c.status = Snapshot{State: state, RequestID: a.id,
		LocalFingerprint: c.local.PublicKey().Fingerprint(), PeerFingerprint: a.peer.Fingerprint(),
		ExpiresAt: a.invite.ExpiresAt().Format(time.RFC3339)}
	return true
}

func (c *Coordinator) run(ctx context.Context, raw net.Conn, invite pairing.Invite, manager *pairing.Manager, role channel.Role) (runErr error) {
	a, err := c.begin(ctx, raw, invite, manager, role)
	if err != nil {
		return err
	}
	stop := context.AfterFunc(a.ctx, a.closeOwned)
	defer func() {
		a.closeOwned()
		stop()
		c.finish(a, runErr)
		close(a.done)
		c.runs.Done()
	}()
	var session *channel.Session
	if role == channel.Inviter {
		session, err = channel.NewServer(a.ctx, raw, c.local, invite.ChannelID(), invite.ExpiresAt())
	} else {
		session, err = channel.NewClient(a.ctx, raw, c.local, invite.Owner(), invite.ChannelID(), invite.ExpiresAt())
	}
	if err != nil || a.setSession(session) != nil {
		return ErrProtocol
	}
	var reservation *channel.Reservation
	if role == channel.Inviter {
		reservation, err = session.AcceptReservation(a.ctx, func(link string) error {
			_, reserveErr := manager.ReserveAuthenticated(link, session)
			return reserveErr
		})
	} else {
		reservation, err = session.RequestReservation(a.ctx, invite.Link())
	}
	if err != nil {
		return ErrProtocol
	}
	a.peer = session.Peer()
	if !c.setState(a, "awaiting-approval") {
		return ErrRequest
	}
	approval, err := reservation.WaitApproval(a.ctx, a.decisions)
	if err != nil {
		return ErrProtocol
	}
	pending, err := transport.ExchangePending(a.ctx, reservation, approval, func(record storage.PendingRecord) error {
		if !c.setState(a, "saving") || a.ctx.Err() != nil {
			return ErrRequest
		}
		a.pendingID = record.ID
		if err := c.store.SavePendingContact(record); err != nil {
			return ErrStore
		}
		a.saved = true
		return a.ctx.Err()
	})
	if err != nil {
		return ErrProtocol
	}
	pending.Revoke() // The encrypted pending record is inert; no live owner escapes.
	return nil
}

func (c *Coordinator) finish(a *attempt, runErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != a {
		return
	}
	expires := a.invite.ExpiresAt()
	a.invite = pairing.Invite{} // Retain no known bearer copy in saved pending UI state.
	if c.closed {
		c.status = Snapshot{State: "unavailable"}
		return
	}
	if a.explicitDeny {
		c.status = Snapshot{State: "canceled"}
		return
	}
	if a.saved {
		// Even a lost remote ACK leaves an inert record to reconcile/discard.
		// No invitation is restarted and no "active" capability is constructed.
		c.status = Snapshot{State: "pending", RequestID: a.id,
			LocalFingerprint: c.local.PublicKey().Fingerprint(), PeerFingerprint: a.peer.Fingerprint()}
		return
	}
	state := "failed"
	if !time.Now().Before(expires) {
		state = "expired"
	}
	if runErr == nil {
		state = "idle"
	}
	c.status, c.current = Snapshot{State: state}, nil
}

// Approve records one explicit decision and returns promptly. The bounded
// background run waits for the peer; stale requests cannot approve another key.
func (c *Coordinator) Approve(id, fingerprint string) (Snapshot, error) {
	if c == nil || c.coordinatorState == nil {
		return Snapshot{State: "unavailable"}, ErrUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.current
	if c.closed || a == nil || c.status.State != "awaiting-approval" || id != a.id || a.ctx.Err() != nil || !time.Now().Before(a.invite.ExpiresAt()) || len(fingerprint) != len(c.status.PeerFingerprint) || subtle.ConstantTimeCompare([]byte(fingerprint), []byte(c.status.PeerFingerprint)) != 1 {
		return c.status, ErrRequest
	}
	c.status.State = "waiting-for-peer"
	a.decisions <- channel.Decision{Approve: true, Fingerprint: fingerprint}
	return c.status, nil
}

// Deny closes transport immediately, waits for its one persistence callback to
// finish, then discards any local pending record. Removing before that join
// would let a late successful save resurrect the canceled record.
func (c *Coordinator) Deny(id string) (Snapshot, error) {
	if c == nil || c.coordinatorState == nil {
		return Snapshot{State: "unavailable"}, ErrUnavailable
	}
	c.mu.Lock()
	a := c.current
	if c.closed || a == nil || id != a.id || c.status.RequestID != id {
		status := c.status
		c.mu.Unlock()
		return status, ErrRequest
	}
	c.actions.Add(1)
	a.explicitDeny = true
	c.status = Snapshot{State: "canceled"}
	c.mu.Unlock()
	defer c.actions.Done()
	a.closeOwned()
	<-a.done
	var err error
	if a.pendingID != "" {
		err = c.store.RemovePendingContact(a.pendingID)
	}
	var remaining []storage.PendingRecord
	if err == nil {
		remaining, err = c.store.PendingContacts()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed && c.current == a {
		c.current = nil
		if err != nil {
			c.status = Snapshot{State: "failed"}
		} else if c.restorePending(remaining) != nil {
			err = ErrStore
			c.status = Snapshot{State: "failed"}
		}
	}
	if err != nil {
		return c.status, ErrStore
	}
	return c.status, nil
}

// Close must finish before the owning vault is locked. Pending records survive
// shutdown as inert data, without resuming an invite, approval or tunnel.
func (c *Coordinator) Close() error {
	if c == nil || c.coordinatorState == nil {
		return nil
	}
	c.mu.Lock()
	c.closed = true
	a := c.current
	c.status = Snapshot{State: "unavailable"}
	c.mu.Unlock()
	if a != nil {
		a.closeOwned()
	}
	c.runs.Wait()
	c.actions.Wait()
	if a != nil && a.closeErr != nil {
		return ErrProtocol
	}
	return nil
}
