// Package messaging connects one durably approved conversation to fresh,
// automatically authenticated contact sessions. Its snapshots are public DTOs;
// credentials, selected service and transport addresses stay in Go.
package messaging

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"net"
	"sync"
	"time"

	"whatsup/internal/chat"
	"whatsup/internal/identity"
	"whatsup/internal/pairing"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

var ErrUnavailable = errors.New("approved conversation is unavailable")

var errPeerCapacity = errors.New("authenticated peer conversation capacity reached")

type Snapshot struct {
	State           string         `json:"state"`
	Revision        uint64         `json:"revision"`
	ContactID       string         `json:"contactID"`
	PeerFingerprint string         `json:"peerFingerprint"`
	PeerDisplayName string         `json:"peerDisplayName"`
	LocalName       string         `json:"localName"`
	CanConnect      bool           `json:"canConnect"`
	CanSend         bool           `json:"canSend"`
	Messages        []chat.Message `json:"messages"`
}

type Listener interface {
	Accept(context.Context) (net.Conn, error)
	Close() error
}

// Factories are Go-only dependency boundaries used by disposable integration
// tests. The production service always uses the reviewed transport constructors.
type Factories struct {
	Profile func(context.Context, string) (*transport.RelayProfile, error)
	Listen  func(context.Context, storage.PendingRecord, *transport.RelayProfile) (Listener, error)
	Dial    func(context.Context, storage.PendingRecord, *transport.RelayProfile) (net.Conn, error)
}

// Manager copies share the private state and lifecycle. Keeping its mutex
// behind this pointer also makes value-format redaction safe without copylocks.
type Manager struct{ *managerState }

type managerState struct {
	mu            sync.Mutex
	local         *identity.Identity
	application   *storage.ApplicationState
	record        storage.PendingRecord
	contact       *pairing.Contact
	conversation  *chat.Conversation
	state         string
	revision      uint64
	closed        bool
	running       bool
	ctx           context.Context
	cancel        context.CancelFunc
	raw           net.Conn
	sessionActive bool
	listener      Listener
	wake          chan struct{}
	workers       sync.WaitGroup
	factories     Factories
	// Used only by the network worker. A successful authentication resets the
	// transport retry schedule; approved relay maps are reused only while valid.
	authenticated uint64
}

func (Manager) String() string               { return "[private WhatsUp messaging coordinator]" }
func (Manager) GoString() string             { return "[private WhatsUp messaging coordinator]" }
func (Manager) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func New(local *identity.Identity, app *storage.ApplicationState, factories ...Factories) (*Manager, error) {
	if local == nil || app == nil || len(factories) > 1 {
		return nil, ErrUnavailable
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{managerState: &managerState{local: local, application: app, state: "none", revision: 1, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}}
	m.factories = Factories{
		Profile: transport.FetchRelayProfile,
		Listen: func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (Listener, error) {
			return transport.NewOngoingListener(ctx, transport.OngoingOptions{Record: record, Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: record.RelayFallbackConsent})
		},
		Dial: func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (net.Conn, error) {
			return transport.DialOngoing(ctx, record, profile, transport.RelayFallback, record.RelayFallbackConsent)
		},
	}
	if len(factories) != 0 {
		if factories[0].Profile != nil {
			m.factories.Profile = factories[0].Profile
		}
		if factories[0].Listen != nil {
			m.factories.Listen = factories[0].Listen
		}
		if factories[0].Dial != nil {
			m.factories.Dial = factories[0].Dial
		}
	}
	if m.refreshLocked() != nil {
		cancel()
		return nil, ErrUnavailable
	}
	return m, nil
}

func (m *Manager) refreshLocked() error {
	if m.closed {
		return ErrUnavailable
	}
	records, err := m.application.CommittedContacts()
	if err != nil {
		m.state = "failed"
		return ErrUnavailable
	}
	if len(records) > 1 {
		m.state = "failed"
		return ErrUnavailable
	}
	if m.record.ID != "" {
		if len(records) != 1 || records[0] != m.record {
			m.state = "failed"
			if m.contact != nil {
				m.contact.Revoke()
			}
			m.cancel()
			return ErrUnavailable
		}
		if m.conversation == nil && !m.running && m.state != "failed" {
			if receipt, err := m.application.LocalConversation(m.record); err == nil {
				contact, err := pairing.RestoreLocalContact(receipt)
				if err != nil || m.openConversationLocked(contact) != nil {
					m.state = "failed"
					return ErrUnavailable
				}
			}
		}
		return nil
	}
	if len(records) == 0 || records[0].Version != 2 {
		return nil
	}
	record := records[0]
	if storage.ValidatePendingRecord(record) != nil || !record.Local.Equal(m.local.PublicKey()) {
		return ErrUnavailable
	}
	m.record, m.state = record, "offline"
	m.revision++
	// Only a durable LOCAL conversation approval restores history/offline queue.
	// An unfinished committed record can bootstrap fresh authentication, but has
	// no local chat authority until that succeeds.
	receipt, err := m.application.LocalConversation(record)
	if err == nil {
		contact, err := pairing.RestoreLocalContact(receipt)
		if err != nil || m.openConversationLocked(contact) != nil {
			return ErrUnavailable
		}
	}
	return nil
}

func (m *Manager) openConversationLocked(contact *pairing.Contact) error {
	store, err := m.application.ChatStore(contact)
	if err != nil {
		contact.Revoke()
		return ErrUnavailable
	}
	conversation, err := chat.Open(store, contact, chat.Options{})
	if err != nil {
		contact.Revoke()
		return ErrUnavailable
	}
	m.contact, m.conversation = contact, conversation
	m.revision++
	return nil
}

func (m *Manager) snapshotLocked() Snapshot {
	result := Snapshot{State: m.state, Revision: m.revision, Messages: []chat.Message{}}
	if m.closed {
		result.State = "unavailable"
		return result
	}
	if m.record.ID == "" && m.state == "failed" {
		result.State = "unavailable"
	}
	if m.record.ID != "" {
		result.ContactID, result.PeerFingerprint = m.record.ID, m.record.Peer.Fingerprint()
		if names, err := m.application.ContactNames(m.record.ID); err == nil {
			result.PeerDisplayName, result.LocalName = names.PeerDisplayName, names.LocalName
		}
		result.CanConnect = !m.running && m.ctx.Err() == nil && m.state != "failed" && m.state != "identity-changed"
		result.CanSend = m.conversation != nil && m.state != "failed" && !capacityFailure(m.state)
		if m.conversation != nil && m.state != "failed" {
			messages, pageErr := m.conversation.Page(0, chat.MaxPageMessages)
			usage, usageErr := m.conversation.Usage()
			if pageErr != nil || usageErr != nil {
				m.state = "failed"
				m.revision++
				m.cancel()
				result.State = "failed"
				result.Revision = m.revision
				result.CanSend = false
				result.CanConnect = false
				return result
			}
			result.Messages = append(result.Messages, messages...)
			if usage.Full {
				result.CanSend = false
				if m.state != "identity-changed" && m.state != "peer-capacity-reached" && m.state != "update-required" {
					result.State = "capacity-reached"
				}
			}
		}
	}
	return result
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil || m.managerState == nil {
		return Snapshot{State: "unavailable", Messages: []chat.Message{}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.refreshLocked()
	return m.snapshotLocked()
}

func (m *Manager) Connect(id string) (Snapshot, error) {
	if m == nil || m.managerState == nil {
		return m.Snapshot(), ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocked() != nil || id == "" || m.record.ID != id || m.ctx.Err() != nil || m.state == "failed" || m.state == "identity-changed" {
		return m.snapshotLocked(), ErrUnavailable
	}
	if !m.running {
		m.running, m.state = true, "connecting"
		m.revision++
		m.workers.Add(1)
		go m.run()
	}
	return m.snapshotLocked(), nil
}

func (m *Manager) Send(id, text string) (Snapshot, error) {
	if m == nil || m.managerState == nil {
		return m.Snapshot(), ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocked() != nil || m.record.ID != id || m.conversation == nil || m.state == "failed" || capacityFailure(m.state) {
		return m.snapshotLocked(), ErrUnavailable
	}
	if _, err := m.conversation.Queue(text); err != nil {
		if errors.Is(err, chat.ErrCapacity) {
			m.state = "capacity-reached"
			m.revision++
		}
		if terminalPersistence(err) {
			m.state = "failed"
			m.revision++
			m.cancel()
		}
		return m.snapshotLocked(), err
	}
	m.revision++
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return m.snapshotLocked(), nil
}

func (m *Manager) changed(state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && m.state != "failed" {
		m.state = state
		m.revision++
	}
}

// MetadataChanged publishes a completed local metadata save and wakes only an
// already running session. It never starts a relay fetch, listener or dial.
func (m *Manager) MetadataChanged() {
	if m == nil || m.managerState == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.revision++
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func retryDelay(count int) time.Duration {
	ceiling := time.Second << min(count, 5)
	if ceiling > 30*time.Second {
		ceiling = 30 * time.Second
	}
	floor := ceiling / 2
	if floor < time.Second {
		floor = time.Second
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(ceiling-floor)+1))
	if err != nil {
		return ceiling
	}
	return floor + time.Duration(n.Int64())
}

func terminalPersistence(err error) bool {
	return errors.Is(err, chat.ErrStore) || errors.Is(err, chat.ErrCorrupt) || errors.Is(err, storage.ErrStateFailed) || errors.Is(err, storage.ErrStateConflict)
}

func capacityFailure(state string) bool {
	return state == "capacity-reached" || state == "peer-capacity-reached"
}

func terminalSession(err error) bool {
	return terminalPersistence(err) || errors.Is(err, sessionauth.ErrIdentityChanged) || errors.Is(err, sessionauth.ErrIncompatible) || errors.Is(err, chat.ErrCapacity) || errors.Is(err, errPeerCapacity)
}

func retryState(err error) string {
	if errors.Is(err, sessionauth.ErrAuthentication) {
		return "authentication-failed"
	}
	return "connecting"
}

func (m *Manager) run() {
	defer m.workers.Done()
	defer func() { m.mu.Lock(); m.running = false; m.revision++; m.mu.Unlock() }()
	var profile *transport.RelayProfile
	for attempt := 0; m.ctx.Err() == nil; attempt++ {
		previousAuthentication := m.authenticated
		var err error
		if !profile.ReadyFor(m.record.RelayServiceURL) {
			profile, err = m.factories.Profile(m.ctx, m.record.RelayServiceURL)
			if err != nil {
				profile = nil
			}
		}
		if err == nil {
			if m.record.Role == "inviter" {
				err = m.serve(profile)
			} else {
				var raw net.Conn
				raw, err = m.factories.Dial(m.ctx, m.record, profile)
				if err == nil {
					err = m.session(raw)
				}
			}
		}
		if m.ctx.Err() != nil {
			return
		}
		if errors.Is(err, sessionauth.ErrIdentityChanged) {
			m.mu.Lock()
			m.state = "identity-changed"
			m.revision++
			m.mu.Unlock()
			m.cancel()
			return
		}
		if errors.Is(err, sessionauth.ErrIncompatible) {
			// The exact saved identity completed TLS while selecting a known
			// previous application protocol. Stop automatic retries, but preserve
			// the runtime so an explicit Connect can retry after updating.
			m.changed("update-required")
			return
		}
		if terminalPersistence(err) {
			m.mu.Lock()
			m.state = "failed"
			m.revision++
			m.mu.Unlock()
			m.cancel()
			return
		}
		if errors.Is(err, chat.ErrCapacity) || errors.Is(err, errPeerCapacity) {
			state := "capacity-reached"
			if errors.Is(err, errPeerCapacity) {
				state = "peer-capacity-reached"
			}
			m.changed(state)
			return
		}
		if m.authenticated != previousAuthentication {
			attempt = 0
		}
		m.changed(retryState(err))
		// Transport failures retry automatically, never prompt for another
		// invitation or silently choose a different relay provider.
		timer := time.NewTimer(retryDelay(attempt))
		select {
		case <-m.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *Manager) serve(profile *transport.RelayProfile) error {
	listener, err := m.factories.Listen(m.ctx, m.record, profile)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = listener.Close()
		return ErrUnavailable
	}
	m.listener = listener
	m.mu.Unlock()
	defer func() { _ = listener.Close(); m.mu.Lock(); m.listener = nil; m.mu.Unlock() }()
	for m.ctx.Err() == nil {
		raw, err := listener.Accept(m.ctx)
		if err != nil {
			return err
		}
		err = m.session(raw)
		if terminalSession(err) {
			return err
		}
		if m.ctx.Err() != nil {
			return m.ctx.Err()
		}
		m.changed(retryState(err))
		if !profile.Ready() {
			return ErrUnavailable
		}
	}
	return ErrUnavailable
}

func (m *Manager) session(raw net.Conn) error {
	ctx, cancel := context.WithCancel(m.ctx)
	defer cancel()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = raw.Close()
		return ErrUnavailable
	}
	m.raw = raw
	m.mu.Unlock()
	defer func() { _ = raw.Close(); m.mu.Lock(); m.raw = nil; m.sessionActive = false; m.mu.Unlock() }()
	binding, err := storage.ContactBinding(m.record)
	if err != nil {
		return ErrUnavailable
	}
	session, err := sessionauth.Authenticate(ctx, raw, sessionauth.Options{Local: m.local, Peer: m.record.Peer, ContactID: m.record.ID, Association: binding, Server: m.record.Role == "inviter"})
	if err != nil {
		return err
	}
	defer session.Close()
	contact, err := pairing.ReconfirmAuthenticated(session, m.record)
	if err != nil {
		return err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		contact.Revoke()
		return ErrUnavailable
	}
	if m.conversation == nil {
		if m.application.ApproveCommittedConversation(m.record, contact) != nil || m.openConversationLocked(contact) != nil {
			m.state = "failed"
			m.revision++
			m.mu.Unlock()
			contact.Revoke()
			m.cancel()
			return ErrUnavailable
		}
	} else {
		contact.Revoke()
	}
	conversation := m.conversation
	if err := conversation.ResetRetries(m.record.Peer); err != nil {
		m.mu.Unlock()
		return err
	}
	m.authenticated++
	m.state = "connected"
	m.sessionActive = true
	m.revision++
	m.mu.Unlock()
	return m.exchange(ctx, session, conversation)
}

// Close joins all network/persistence work before the service closes the vault.
// The same approved record remains durable for normal future reconnects.
func (m *Manager) Close() {
	if m == nil || m.managerState == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	m.state = "unavailable"
	m.revision++
	raw, listener, contact := m.raw, m.listener, m.contact
	m.mu.Unlock()
	m.cancel()
	if raw != nil {
		_ = raw.Close()
	}
	if listener != nil {
		_ = listener.Close()
	}
	m.workers.Wait()
	if contact != nil {
		contact.Revoke()
	}
	m.mu.Lock()
	m.conversation = nil
	m.contact = nil
	m.mu.Unlock()
}
