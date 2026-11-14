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
	"whatsup/internal/diagnostics"
	"whatsup/internal/identity"
	"whatsup/internal/pairing"
	"whatsup/internal/sessionauth"
	"whatsup/internal/storage"
	"whatsup/internal/transfer"
	"whatsup/internal/transport"
)

var ErrUnavailable = errors.New("approved conversation is unavailable")

var errPeerCapacity = errors.New("authenticated peer conversation capacity reached")

type Snapshot struct {
	State                    string         `json:"state"`
	Path                     string         `json:"path"`
	Revision                 uint64         `json:"revision"`
	ContactID                string         `json:"contactID"`
	PeerFingerprint          string         `json:"peerFingerprint"`
	PeerDisplayName          string         `json:"peerDisplayName"`
	LocalName                string         `json:"localName"`
	CanConnect               bool           `json:"canConnect"`
	CanSend                  bool           `json:"canSend"`
	PeerProtocol             uint8          `json:"peerProtocol"`
	AttachmentsSupported     bool           `json:"attachmentsSupported"`
	ContactControlsSupported bool           `json:"contactControlsSupported"`
	Messages                 []chat.Message `json:"messages"`
}

type Listener interface {
	Accept(context.Context) (net.Conn, error)
	Close() error
}

// Factories are Go-only dependency boundaries used by disposable integration
// tests. The production service always uses the reviewed transport constructors.
type Factories struct {
	Profile         func(context.Context, string) (*transport.RelayProfile, error)
	Listen          func(context.Context, storage.PendingRecord, *transport.RelayProfile) (Listener, error)
	Dial            func(context.Context, storage.PendingRecord, *transport.RelayProfile) (net.Conn, error)
	PreferredRegion func(*transport.RelayProfile) (uint16, error)
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
	transfers     *transfer.Engine
	state         string
	revision      uint64
	targetID      string
	revisions     *Revisions
	onChanged     func(Hint)
	onIncoming    func(Incoming)
	diagnostics   func(diagnostics.Event)
	path          transport.Path
	pathAt        time.Time
	pathTimer     *time.Timer
	pathEpoch     uint64
	closed        bool
	running       bool
	ctx           context.Context
	cancel        context.CancelFunc
	raw           net.Conn
	sessionActive bool
	capabilities  sessionauth.Capabilities
	controls      *controlSession
	peerRemoved   bool
	removing      bool
	listener      Listener
	wake          chan struct{}
	workers       sync.WaitGroup
	factories     Factories
	profiles      map[string]*transport.RelayProfile
	profileOrder  []string
	routeRefresh  sync.Mutex
	routeRestart  chan *routeRestartRequest
	routeCancel   context.CancelFunc
	routeDone     chan struct{}
	runDone       chan struct{}
	attempts      map[*routeAttempt]struct{}
	listeners     map[*routeListener]struct{}
	claim         *routeClaim
	intentRunning bool
	intentAgain   bool
	planKey       [32]byte
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
	options := ContactOptions{}
	if len(factories) == 1 {
		options.Factories = factories[0]
	}
	return newManager(local, app, "", options)
}

// NewContact owns only the exact saved association. It never selects the first
// record or inherits a replacement association for the same identity.
func NewContact(local *identity.Identity, app *storage.ApplicationState, id string, options ...ContactOptions) (*Manager, error) {
	if local == nil || app == nil || id == "" || len(options) > 1 {
		return nil, ErrUnavailable
	}
	var selected ContactOptions
	if len(options) == 1 {
		selected = options[0]
	}
	return newManager(local, app, id, selected)
}

type ContactOptions struct {
	Revisions   *Revisions
	OnChanged   func(Hint)
	OnIncoming  func(Incoming)
	Diagnostics func(diagnostics.Event)
	Factories   Factories
}

func newManager(local *identity.Identity, app *storage.ApplicationState, id string, options ContactOptions) (*Manager, error) {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{managerState: &managerState{local: local, application: app, targetID: id, revisions: options.Revisions, onChanged: options.OnChanged, onIncoming: options.OnIncoming, diagnostics: diagnostics.RateLimited(options.Diagnostics), path: transport.PathUnknown, state: "none", revision: 1, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), routeRestart: make(chan *routeRestartRequest, 1), profiles: map[string]*transport.RelayProfile{}, attempts: map[*routeAttempt]struct{}{}}}
	m.factories = Factories{
		Profile:         transport.FetchRelayProfile,
		PreferredRegion: func(profile *transport.RelayProfile) (uint16, error) { return profile.PreferredRegion() },
		Listen: func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (Listener, error) {
			return transport.NewOngoingListener(ctx, transport.OngoingOptions{Record: record, Profile: profile, Policy: transport.RelayFallback, AllowRelayFallback: record.RelayFallbackConsent})
		},
		Dial: func(ctx context.Context, record storage.PendingRecord, profile *transport.RelayProfile) (net.Conn, error) {
			return transport.DialOngoing(ctx, record, profile, transport.RelayFallback, record.RelayFallbackConsent)
		},
	}
	if options.Factories.Profile != nil {
		m.factories.Profile = options.Factories.Profile
	}
	if options.Factories.Listen != nil {
		m.factories.Listen = options.Factories.Listen
	}
	if options.Factories.Dial != nil {
		m.factories.Dial = options.Factories.Dial
	}
	if options.Factories.PreferredRegion != nil {
		m.factories.PreferredRegion = options.Factories.PreferredRegion
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
		m.failLocked()
		return ErrUnavailable
	}
	if m.targetID != "" {
		selected := records[:0]
		for _, record := range records {
			if record.ID == m.targetID {
				selected = append(selected, record)
			}
		}
		records = selected
		if len(records) != 1 {
			m.failLocked()
			return ErrUnavailable
		}
	} else if len(records) > 1 {
		m.failLocked()
		return ErrUnavailable
	}
	if m.record.ID != "" {
		if len(records) != 1 || records[0] != m.record {
			m.failLocked()
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
					m.failLocked()
					return ErrUnavailable
				}
			}
		}
		return m.refreshRemovalLocked()
	}
	if len(records) == 0 || records[0].Version != 2 {
		if m.targetID != "" {
			return ErrUnavailable
		}
		return nil
	}
	record := records[0]
	if storage.ValidatePendingRecord(record) != nil || !record.Local.Equal(m.local.PublicKey()) {
		return ErrUnavailable
	}
	m.record, m.state = record, "offline"
	m.bumpLocked()
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
	return m.refreshRemovalLocked()
}

func (m *Manager) failLocked() {
	if m.state != "failed" {
		m.state = "failed"
		m.bumpLocked()
	}
	m.cancel()
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
	m.bumpLocked()
	return nil
}

func (m *Manager) snapshotLocked(history ...bool) Snapshot {
	result := Snapshot{State: m.state, Path: string(m.pathLocked(time.Now())), Revision: m.revision, Messages: []chat.Message{}}
	if m.closed {
		result.State = "unavailable"
		return result
	}
	if m.record.ID == "" && m.state == "failed" {
		result.State = "unavailable"
	}
	if m.record.ID != "" {
		result.PeerProtocol = m.capabilities.WireVersion
		result.AttachmentsSupported = m.sessionActive && m.capabilities.Attachments && !m.peerRemoved && !m.removing
		result.ContactControlsSupported = m.sessionActive && m.capabilities.Control && !m.peerRemoved && !m.removing
		result.ContactID, result.PeerFingerprint = m.record.ID, m.record.Peer.Fingerprint()
		if names, err := m.application.ContactNames(m.record.ID); err == nil {
			result.PeerDisplayName, result.LocalName = names.PeerDisplayName, names.LocalName
		}
		result.CanConnect = !m.running && m.ctx.Err() == nil && m.state != "failed" && m.state != "identity-changed" && !m.peerRemoved && !m.removing
		result.CanSend = m.conversation != nil && m.state != "failed" && !capacityFailure(m.state) && !m.peerRemoved && !m.removing
		if m.conversation != nil && m.state != "failed" {
			messages, pageErr := []chat.Message{}, error(nil)
			if len(history) == 0 || history[0] {
				messages, pageErr = m.conversation.Page(0, chat.MaxPageMessages)
			}
			usage, usageErr := m.conversation.Usage()
			if pageErr != nil || usageErr != nil {
				m.state = "failed"
				m.bumpLocked()
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
				if m.state != "identity-changed" && m.state != "peer-capacity-reached" && m.state != "update-required" && !m.peerRemoved {
					result.State = "capacity-reached"
				}
			}
		}
	}
	return result
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil || m.managerState == nil {
		return Snapshot{State: "unavailable", Path: string(transport.PathUnknown), Messages: []chat.Message{}}
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
	if m.refreshLocked() != nil || id == "" || m.record.ID != id || m.ctx.Err() != nil || m.state == "failed" || m.state == "identity-changed" || m.peerRemoved || m.removing {
		return m.snapshotLocked(), ErrUnavailable
	}
	if !m.running {
		m.running, m.state = true, "connecting"
		m.runDone = make(chan struct{})
		m.bumpLocked()
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
	var report diagnostics.Event
	defer func() { m.mu.Unlock(); m.report(report) }()
	if m.refreshLocked() != nil || m.record.ID != id || m.conversation == nil || m.state == "failed" || capacityFailure(m.state) || m.peerRemoved || m.removing {
		return m.snapshotLocked(), ErrUnavailable
	}
	if _, err := m.conversation.Queue(text); err != nil {
		if errors.Is(err, chat.ErrCapacity) {
			report = diagnostics.CapacityReached
			m.state = "capacity-reached"
			m.bumpLocked()
		}
		if terminalPersistence(err) {
			m.state = "failed"
			m.bumpLocked()
			m.cancel()
		}
		return m.snapshotLocked(), err
	}
	m.bumpLocked()
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
		m.bumpLocked()
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
	m.bumpLocked()
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
	return terminalPersistence(err) || errors.Is(err, sessionauth.ErrIdentityChanged) || errors.Is(err, sessionauth.ErrIncompatible) || errors.Is(err, chat.ErrCapacity) || errors.Is(err, errPeerCapacity) || errors.Is(err, errPeerRemoved) || errors.Is(err, errRemovalSent)
}

func retryState(err error) string {
	if errors.Is(err, sessionauth.ErrAuthentication) {
		return "authentication-failed"
	}
	return "connecting"
}

func (m *Manager) run() {
	defer m.workers.Done()
	m.startRelayIntent()
	defer func() {
		m.mu.Lock()
		m.running = false
		done := m.runDone
		m.bumpLocked()
		m.mu.Unlock()
		if done != nil {
			close(done)
		}
	}()
	for attempt := 0; m.ctx.Err() == nil; attempt++ {
		previousAuthentication := m.authenticated
		err := m.runRouteCycle()
		if m.ctx.Err() != nil {
			return
		}
		if m.authenticated != previousAuthentication {
			attempt = 0
		}
		if errors.Is(err, errRoutesChanged) {
			// A planned generation replacement is not a failed connection. The
			// loop post must not advance backoff, including after fresh TLS.
			attempt--
			continue
		}
		if errors.Is(err, errPeerRemoved) {
			m.changed("peer-removed")
			return
		}
		if errors.Is(err, errRemovalSent) {
			return
		}
		if errors.Is(err, sessionauth.ErrIdentityChanged) {
			m.report(diagnostics.IdentityChanged)
			m.mu.Lock()
			m.state = "identity-changed"
			m.bumpLocked()
			m.mu.Unlock()
			m.cancel()
			return
		}
		if errors.Is(err, sessionauth.ErrIncompatible) {
			// The exact saved identity completed TLS while selecting a known
			// unsupported application protocol. Stop automatic retries, but preserve
			// the runtime so an explicit Connect can retry after updating.
			m.changed("update-required")
			return
		}
		if terminalPersistence(err) {
			m.mu.Lock()
			m.state = "failed"
			m.bumpLocked()
			m.mu.Unlock()
			m.cancel()
			return
		}
		if errors.Is(err, chat.ErrCapacity) || errors.Is(err, errPeerCapacity) {
			m.report(diagnostics.CapacityReached)
			state := "capacity-reached"
			if errors.Is(err, errPeerCapacity) {
				state = "peer-capacity-reached"
			}
			m.changed(state)
			return
		}
		m.changed(retryState(err))
		// Transport failures retry automatically, never prompt for another
		// invitation or silently choose a different relay provider.
		timer := time.NewTimer(retryDelay(attempt))
		select {
		case <-m.ctx.Done():
			timer.Stop()
			return
		case request := <-m.routeRestart:
			timer.Stop()
			request.finish()
		case <-timer.C:
		}
	}
}

// Close joins all network/persistence work before the service closes the vault.
// The same approved record remains durable for normal future reconnects.
func (m *Manager) Close() {
	if m == nil || m.managerState == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	m.capabilities = sessionauth.Capabilities{}
	m.state = "unavailable"
	m.clearPathLocked()
	m.bumpLocked()
	raw, listener, contact := m.raw, m.listener, m.contact
	attempts := make([]net.Conn, 0, len(m.attempts))
	for attempt := range m.attempts {
		attempts = append(attempts, attempt.raw)
	}
	listeners := make([]Listener, 0, len(m.listeners))
	for owned := range m.listeners {
		listeners = append(listeners, owned.listener)
	}
	m.mu.Unlock()
	m.cancel()
	if raw != nil {
		_ = raw.Close()
	}
	if listener != nil {
		_ = listener.Close()
	}
	for _, conn := range attempts {
		_ = conn.Close()
	}
	for _, owned := range listeners {
		_ = owned.Close()
	}
	m.workers.Wait()
	m.mu.Lock()
	transfers := m.transfers
	m.mu.Unlock()
	if transfers != nil {
		transfers.Close()
	}
	if contact != nil {
		contact.Revoke()
	}
	m.mu.Lock()
	m.conversation = nil
	m.contact = nil
	m.mu.Unlock()
}
