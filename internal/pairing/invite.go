// Package pairing implements local invitation state and identity authorization.
// It does not implement a network handshake. Reserve requires the candidate key
// from an authenticated transport, and a contact must not be activated until
// mutual approval and encrypted durable storage succeed.
package pairing

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/url"
	"strconv"
	"sync"
	"time"

	"whatsup/internal/identity"
	"whatsup/internal/transport/address"
)

const (
	DefaultLifetime  = 10 * time.Minute
	MaxLinkBytes     = 2048
	MaxEndpointBytes = address.MaxBytes
)

var (
	ErrInvalidInvite = errors.New("invalid invitation")
	ErrUnavailable   = errors.New("invitation unavailable")
	ErrExpired       = errors.New("invitation expired")
	ErrBusy          = errors.New("invitation already reserved")
	ErrFingerprint   = errors.New("candidate fingerprint does not match")
	ErrListenerClose = errors.New("invitation listener could not be closed")
	ErrClosed        = errors.New("pairing manager closed")
)

type State string

const (
	Idle     State = "idle"
	Created  State = "created"
	Pending  State = "pending approval"
	Consumed State = "consumed"
	Expired  State = "expired"
	Revoked  State = "revoked"
)

// Invite contains a bearer secret. Its String method intentionally redacts it.
// Never persist an invitation, including in a history backup or frontend cache.
type Invite struct {
	id       [16]byte
	secret   [32]byte
	owner    identity.PublicKey
	endpoint address.Endpoint
	expires  time.Time
}

func (i Invite) String() string             { return "[private WhatsUp invitation]" }
func (i Invite) GoString() string           { return i.String() }
func (i Invite) Owner() identity.PublicKey  { return i.owner }
func (i Invite) Endpoint() address.Endpoint { return i.endpoint }
func (i Invite) ExpiresAt() time.Time       { return i.expires }

// Link returns the private bearer deep link only for the explicit share action.
func (i Invite) Link() string {
	query := url.Values{
		"id":       {base64.RawURLEncoding.EncodeToString(i.id[:])},
		"secret":   {base64.RawURLEncoding.EncodeToString(i.secret[:])},
		"owner":    {i.owner.String()},
		"endpoint": {string(i.endpoint.EncodeForTransport())},
		"expires":  {strconv.FormatInt(i.expires.Unix(), 10)},
	}
	return "whatsup://pair/v1?" + query.Encode()
}

// ParseInvite performs bounded, canonical parsing, including the typed Tailcat
// endpoint grammar. It never resolves the endpoint or opens a socket.
func ParseInvite(link string) (Invite, error) {
	var invite Invite
	if len(link) == 0 || len(link) > MaxLinkBytes {
		return invite, ErrInvalidInvite
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "whatsup" || u.Host != "pair" || u.Path != "/v1" || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || u.ForceQuery {
		return invite, ErrInvalidInvite
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 5 {
		return invite, ErrInvalidInvite
	}
	for _, key := range []string{"id", "secret", "owner", "endpoint", "expires"} {
		if len(query[key]) != 1 || query.Get(key) == "" {
			return invite, ErrInvalidInvite
		}
	}
	if !decodeToken(query.Get("id"), invite.id[:]) || !decodeToken(query.Get("secret"), invite.secret[:]) {
		return Invite{}, ErrInvalidInvite
	}
	invite.owner, err = identity.ParsePublicKey(query.Get("owner"))
	if err != nil {
		return Invite{}, ErrInvalidInvite
	}
	invite.endpoint, err = address.Parse(query.Get("endpoint"))
	if err != nil {
		return Invite{}, ErrInvalidInvite
	}
	seconds, err := strconv.ParseInt(query.Get("expires"), 10, 64)
	if err != nil || seconds <= 0 || seconds > 253402300799 {
		return Invite{}, ErrInvalidInvite
	}
	invite.expires = time.Unix(seconds, 0).UTC()
	if invite.Link() != link {
		return Invite{}, ErrInvalidInvite
	}
	return invite, nil
}

func decodeToken(text string, target []byte) bool {
	if len(text) != base64.RawURLEncoding.EncodedLen(len(target)) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(text)
	if err != nil || len(decoded) != len(target) {
		return false
	}
	copy(target, decoded)
	return base64.RawURLEncoding.EncodeToString(target) == text
}

type Options struct {
	// Now supports deterministic boundary tests. The default is time.Now.
	Now func() time.Time
	// CloseInvitation must synchronously retire the invitation listener and its
	// transport credentials. It must not call back into Manager. No contact is
	// returned if this fails. A nil hook is appropriate for domain-only use with
	// no listener; it does not establish any network teardown guarantee.
	CloseInvitation func() error
}

// Snapshot is safe for a frontend: it has neither bearer nor session secrets.
type Snapshot struct {
	State                State     `json:"state"`
	ExpiresAt            time.Time `json:"expiresAt"`
	CandidateFingerprint string    `json:"candidateFingerprint"`
	LifecycleError       string    `json:"lifecycleError"`
}

// Manager is one in-memory invitation lifecycle per conversation. It neither
// persists invites nor accepts them into a new instance after restart.
type Manager struct {
	mu        sync.Mutex
	local     identity.PublicKey
	now       func() time.Time
	close     func() error
	invite    Invite
	state     State
	candidate identity.PublicKey
	timer     *time.Timer
	closed    bool
	closeErr  error
}

func NewManager(local identity.PublicKey, options Options) (*Manager, error) {
	if !local.Valid() {
		return nil, identity.ErrInvalidKey
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Manager{local: local, now: options.Now, close: options.CloseInvitation, state: Idle}, nil
}

func (m *Manager) Create(endpoint string, lifetime time.Duration) (Invite, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Invite{}, ErrClosed
	}
	m.expireLocked()
	if m.closeErr != nil {
		return Invite{}, m.closeErr
	}
	if m.state == Created || m.state == Pending || m.state == Consumed {
		return Invite{}, ErrUnavailable
	}
	if lifetime == 0 {
		lifetime = DefaultLifetime
	}
	parsedEndpoint, err := address.Parse(endpoint)
	if lifetime < time.Second || lifetime > DefaultLifetime || err != nil {
		return Invite{}, ErrInvalidInvite
	}
	invite := Invite{owner: m.local, endpoint: parsedEndpoint, expires: m.now().Add(lifetime).UTC().Truncate(time.Second)}
	if _, err := rand.Read(invite.id[:]); err != nil {
		return Invite{}, err
	}
	if _, err := rand.Read(invite.secret[:]); err != nil {
		return Invite{}, err
	}
	m.invite = invite
	m.candidate = identity.PublicKey{}
	m.state = Created
	inviteID := invite.id
	m.timer = time.AfterFunc(invite.expires.Sub(m.now()), func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		// The elapsed timer also expires the invitation if the operating system
		// wall clock moved backwards. A stopped old timer cannot burn a new invite.
		if m.invite.id == inviteID && (m.state == Created || m.state == Pending) {
			_ = m.finishLocked(Expired)
		}
	})
	return invite, nil
}

// Reserve atomically selects exactly one candidate identity. The caller must
// supply the public key established by an authenticated transport handshake,
// never a peer's unverified JSON claim. Pending state grants no chat capability.
func (m *Manager) Reserve(link string, authenticatedCandidate identity.PublicKey) (Snapshot, error) {
	invite, err := ParseInvite(link)
	if err != nil {
		return Snapshot{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Snapshot{}, ErrClosed
	}
	m.expireLocked()
	if m.state == Expired {
		return Snapshot{}, ErrExpired
	}
	if m.state == Pending {
		return Snapshot{}, ErrBusy
	}
	if m.state != Created {
		return Snapshot{}, ErrUnavailable
	}
	if !authenticatedCandidate.Valid() || authenticatedCandidate.Equal(m.local) {
		return Snapshot{}, identity.ErrInvalidKey
	}
	if invite.id != m.invite.id || !invite.owner.Equal(m.local) || !invite.endpoint.Equal(m.invite.endpoint) || !invite.expires.Equal(m.invite.expires) || subtle.ConstantTimeCompare(invite.secret[:], m.invite.secret[:]) != 1 {
		return Snapshot{}, ErrInvalidInvite
	}
	m.candidate = authenticatedCandidate
	m.state = Pending
	return m.snapshotLocked(), nil
}

// Approve checks the complete fingerprint observed over a trusted channel. The
// returned Contact is a local approval record only; the caller must still obtain
// reciprocal authenticated approval and persist it before opening chat access.
func (m *Manager) Approve(candidateFingerprint string) (*Contact, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	m.expireLocked()
	if m.state == Expired {
		return nil, ErrExpired
	}
	if m.state != Pending {
		return nil, ErrUnavailable
	}
	fingerprint := m.candidate.Fingerprint()
	if len(candidateFingerprint) != len(fingerprint) || subtle.ConstantTimeCompare([]byte(candidateFingerprint), []byte(fingerprint)) != 1 {
		return nil, ErrFingerprint
	}
	contact, err := newContact(m.local, m.candidate)
	if err != nil {
		return nil, err
	}
	if err := m.finishLocked(Consumed); err != nil {
		contact.Revoke()
		m.state = Revoked
		return nil, err
	}
	return contact, nil
}

// Revoke also covers denial and abort: a reserved invite can never be returned
// to Created. A subsequent attempt needs a newly generated invite.
func (m *Manager) Revoke() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrClosed
	}
	m.expireLocked()
	if m.state != Created && m.state != Pending {
		return ErrUnavailable
	}
	return m.finishLocked(Revoked)
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	return m.snapshotLocked()
}

// Close invalidates all invitations for process shutdown. Erasing known arrays
// reduces retention; Go does not promise that prior value/string copies vanish.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return m.closeErr
	}
	m.closed = true
	if m.state == Created || m.state == Pending {
		return m.finishLocked(Revoked)
	}
	return m.closeErr
}

func (m *Manager) snapshotLocked() Snapshot {
	snapshot := Snapshot{State: m.state, ExpiresAt: m.invite.expires}
	if m.closeErr != nil {
		snapshot.LifecycleError = m.closeErr.Error()
	}
	if m.state == Pending {
		snapshot.CandidateFingerprint = m.candidate.Fingerprint()
	}
	return snapshot
}

func (m *Manager) expireLocked() {
	if (m.state == Created || m.state == Pending) && !m.now().Before(m.invite.expires) {
		// Even if close fails, the credential is erased and cannot authorize a
		// contact. Snapshot exposes the failure and new invites remain blocked.
		_ = m.finishLocked(Expired)
	}
}

func (m *Manager) finishLocked(state State) error {
	if m.timer != nil {
		m.timer.Stop()
		m.timer = nil
	}
	m.state = state
	clear(m.invite.secret[:])
	m.invite.endpoint = address.Endpoint{}
	m.candidate = identity.PublicKey{}
	if m.close != nil {
		if err := m.close(); err != nil {
			m.closeErr = ErrListenerClose
			return m.closeErr
		}
	}
	return nil
}
