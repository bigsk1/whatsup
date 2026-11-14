package messaging

import (
	"math"
	"strconv"
	"sync"

	"whatsup/internal/chat"
	"whatsup/internal/diagnostics"
	"whatsup/internal/identity"
	"whatsup/internal/storage"
	"whatsup/internal/transport"
)

// Hint is an invalidation hint, never message content or runtime authority.
type Hint struct {
	ContactID string `json:"contactID"`
	Revision  uint64 `json:"revision"`
}

// Revisions is shared for the Service lifetime, including lock/unlock cycles.
// A delayed event from a retired generation cannot outrank a new snapshot.
type Revisions struct {
	mu     sync.Mutex
	values map[string]uint64
}

func (r *Revisions) next(id string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]uint64)
	}
	r.values[id]++
	return r.values[id]
}

func (m *Manager) bumpLocked() {
	if m.revisions != nil && m.record.ID != "" {
		m.revision = m.revisions.next(m.record.ID)
	} else {
		m.revision++
	}
	if m.onChanged != nil && m.record.ID != "" {
		// This sink only enqueues; application event hooks never run here.
		m.onChanged(Hint{ContactID: m.record.ID, Revision: m.revision})
	}
}

type ContactSummary struct {
	State                    string `json:"state"`
	Path                     string `json:"path"`
	Revision                 uint64 `json:"revision"`
	ContactID                string `json:"contactID"`
	PeerFingerprint          string `json:"peerFingerprint"`
	PeerDisplayName          string `json:"peerDisplayName"`
	LocalName                string `json:"localName"`
	CanConnect               bool   `json:"canConnect"`
	CanSend                  bool   `json:"canSend"`
	PeerProtocol             uint8  `json:"peerProtocol"`
	AttachmentsSupported     bool   `json:"attachmentsSupported"`
	ContactControlsSupported bool   `json:"contactControlsSupported"`
	Unread                   uint64 `json:"unread"`
}

type MessagePage struct {
	ContactID  string         `json:"contactID"`
	Revision   uint64         `json:"revision"`
	Messages   []chat.Message `json:"messages"`
	NextCursor string         `json:"nextCursor"`
	HasMore    bool           `json:"hasMore"`
}

func (m *Manager) Summary() ContactSummary {
	if m == nil || m.managerState == nil {
		return ContactSummary{State: "unavailable", Path: string(transport.PathUnknown)}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = m.refreshLocked()
	return m.summaryLocked()
}

func (m *Manager) summaryLocked() ContactSummary {
	s := m.snapshotLocked(false)
	summary := ContactSummary{State: s.State, Path: s.Path, Revision: s.Revision, ContactID: s.ContactID, PeerFingerprint: s.PeerFingerprint, PeerDisplayName: s.PeerDisplayName, LocalName: s.LocalName, CanConnect: s.CanConnect, CanSend: s.CanSend, PeerProtocol: s.PeerProtocol, AttachmentsSupported: s.AttachmentsSupported, ContactControlsSupported: s.ContactControlsSupported}
	if !m.closed && m.conversation != nil && m.state != "failed" {
		unread, err := m.conversation.UnreadCount()
		if err != nil {
			m.state = "failed"
			m.cancel()
			m.bumpLocked()
			summary.State, summary.Revision, summary.CanConnect, summary.CanSend = "failed", m.revision, false, false
		} else {
			summary.Unread = unread
		}
	}
	return summary
}

func (m *Manager) Page(id, cursor string, limit int) (MessagePage, error) {
	result := MessagePage{ContactID: id, Messages: []chat.Message{}}
	var before uint64
	if cursor != "" {
		var err error
		before, err = strconv.ParseUint(cursor, 10, 63)
		if err != nil || before == 0 || before > math.MaxInt64 || strconv.FormatUint(before, 10) != cursor {
			return result, ErrUnavailable
		}
	}
	if m == nil || m.managerState == nil || limit < 1 || limit > chat.MaxPageMessages {
		return result, ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocked() != nil || m.record.ID != id || m.conversation == nil || m.state == "failed" {
		return result, ErrUnavailable
	}
	page, err := m.conversation.ReadPage(before, limit)
	if err != nil {
		_, _ = m.historyFailureLocked(err)
		return result, err
	}
	result.Revision, result.Messages, result.HasMore = m.revision, page.Messages, page.HasMore
	if result.Messages == nil {
		result.Messages = []chat.Message{}
	}
	if page.HasMore {
		result.NextCursor = strconv.FormatUint(page.NextCursor, 10)
	}
	return result, nil
}

func (m *Manager) MarkRead(id, throughMessageID string) (ContactSummary, error) {
	if m == nil || m.managerState == nil {
		return ContactSummary{State: "unavailable", Path: string(transport.PathUnknown)}, ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.refreshLocked() != nil || m.record.ID != id || m.conversation == nil || m.state == "failed" {
		return m.summaryLocked(), ErrUnavailable
	}
	if err := m.conversation.MarkRead(throughMessageID); err != nil {
		_, _ = m.historyFailureLocked(err)
		return m.summaryLocked(), err
	}
	m.bumpLocked()
	return m.summaryLocked(), nil
}

type RegistryOptions struct {
	Revisions *Revisions
	// These backend-only sinks must enqueue and return without acquiring the
	// owning Service mutex. Incoming carries IDs only, after durable insertion.
	OnIncoming  func(Incoming)
	Diagnostics func(diagnostics.Event)
	// OnChanged must enqueue without taking the owning Service mutex. Close
	// joins this callback so it cannot outlive the runtime generation.
	OnChanged func(Hint)
	Factories Factories
}

// Registry owns independent approved-contact runtimes for one unlocked vault.
// Retired IDs remain tombstoned for this generation, including late completion.
type Registry struct{ *registryState }
type registryState struct {
	mu          sync.Mutex
	local       *identity.Identity
	application *storage.ApplicationState
	managers    map[string]*Manager
	retired     map[string]bool
	closed      bool
	options     RegistryOptions
	notifyMu    sync.Mutex
	dirty       map[string]Hint
	notify      chan struct{}
	stop        chan struct{}
	done        chan struct{}
	retirees    sync.WaitGroup
}

func (Registry) String() string               { return "[private WhatsUp contact runtimes]" }
func (Registry) GoString() string             { return "[private WhatsUp contact runtimes]" }
func (Registry) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func NewRegistry(local *identity.Identity, app *storage.ApplicationState, options ...RegistryOptions) (*Registry, error) {
	if local == nil || app == nil || len(options) > 1 {
		return nil, ErrUnavailable
	}
	var selected RegistryOptions
	if len(options) == 1 {
		selected = options[0]
	}
	if selected.Revisions == nil {
		selected.Revisions = &Revisions{}
	}
	selected.Diagnostics = diagnostics.RateLimited(selected.Diagnostics)
	r := &Registry{&registryState{local: local, application: app, options: selected, managers: map[string]*Manager{}, retired: map[string]bool{}, dirty: map[string]Hint{}, notify: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}}
	go r.dispatch()
	if _, err := r.Contacts(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (r *Registry) enqueue(h Hint) {
	r.notifyMu.Lock()
	if old := r.dirty[h.ContactID]; h.Revision > old.Revision {
		r.dirty[h.ContactID] = h
	}
	r.notifyMu.Unlock()
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *Registry) dispatch() {
	defer close(r.done)
	for {
		select {
		case <-r.stop:
			return
		case <-r.notify:
		}
		r.notifyMu.Lock()
		pending := r.dirty
		r.dirty = map[string]Hint{}
		r.notifyMu.Unlock()
		for _, hint := range pending {
			select {
			case <-r.stop:
				return
			default:
			}
			if r.options.OnChanged != nil {
				r.options.OnChanged(hint)
			}
		}
	}
}

func (r *Registry) ensureLocked(id string) (*Manager, error) {
	if r.closed || id == "" || r.retired[id] {
		return nil, ErrUnavailable
	}
	if m := r.managers[id]; m != nil {
		return m, nil
	}
	if len(r.managers) >= storage.MaxContacts {
		return nil, ErrUnavailable
	}
	m, err := NewContact(r.local, r.application, id, ContactOptions{Revisions: r.options.Revisions, OnChanged: r.enqueue, OnIncoming: r.options.OnIncoming, Diagnostics: r.options.Diagnostics, Factories: r.options.Factories})
	if err != nil {
		return nil, err
	}
	r.managers[id] = m
	return m, nil
}

func (r *Registry) manager(id string) (*Manager, error) {
	if r == nil || r.registryState == nil {
		return nil, ErrUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ensureLocked(id)
}

func (r *Registry) Contacts() ([]ContactSummary, error) {
	if r == nil || r.registryState == nil {
		return nil, ErrUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, ErrUnavailable
	}
	records, err := r.application.CommittedContacts()
	if err != nil || len(records) > storage.MaxContacts {
		return nil, ErrUnavailable
	}
	result := make([]ContactSummary, 0, len(records))
	for _, record := range records {
		if record.Version != 2 || r.retired[record.ID] {
			continue
		}
		m, err := r.ensureLocked(record.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, m.Summary())
	}
	return result, nil
}

func (r *Registry) Snapshot(id string) Snapshot {
	m, err := r.manager(id)
	if err != nil {
		return Snapshot{State: "unavailable", Path: string(transport.PathUnknown), Messages: []chat.Message{}}
	}
	return m.Snapshot()
}
func (r *Registry) Connect(id string) (Snapshot, error) {
	m, err := r.manager(id)
	if err != nil {
		return Snapshot{State: "unavailable", Path: string(transport.PathUnknown), Messages: []chat.Message{}}, err
	}
	return m.Connect(id)
}
func (r *Registry) Send(id, text string) (Snapshot, error) {
	m, err := r.manager(id)
	if err != nil {
		return Snapshot{State: "unavailable", Path: string(transport.PathUnknown), Messages: []chat.Message{}}, err
	}
	return m.Send(id, text)
}
func (r *Registry) ClearHistory(id string) (Snapshot, error) {
	m, err := r.manager(id)
	if err != nil {
		return Snapshot{State: "unavailable", Path: string(transport.PathUnknown), Messages: []chat.Message{}}, err
	}
	return m.ClearHistory(id)
}
func (r *Registry) DeleteMessage(id, messageID string) (Snapshot, error) {
	m, err := r.manager(id)
	if err != nil {
		return Snapshot{State: "unavailable", Path: string(transport.PathUnknown), Messages: []chat.Message{}}, err
	}
	return m.DeleteMessage(id, messageID)
}
func (r *Registry) Page(id, cursor string, limit int) (MessagePage, error) {
	m, err := r.manager(id)
	if err != nil {
		return MessagePage{ContactID: id, Messages: []chat.Message{}}, err
	}
	return m.Page(id, cursor, limit)
}
func (r *Registry) MarkRead(id, throughMessageID string) (ContactSummary, error) {
	m, err := r.manager(id)
	if err != nil {
		return ContactSummary{State: "unavailable", Path: string(transport.PathUnknown)}, err
	}
	return m.MarkRead(id, throughMessageID)
}

func (r *Registry) MetadataChanged(id string) {
	if r == nil || r.registryState == nil {
		return
	}
	r.mu.Lock()
	var managers []*Manager
	if !r.closed {
		for key, m := range r.managers {
			if id == "" || key == id {
				managers = append(managers, m)
			}
		}
	}
	r.mu.Unlock()
	for _, m := range managers {
		m.MetadataChanged()
	}
}

func (r *Registry) Retire(id string) error {
	if r == nil || r.registryState == nil || id == "" {
		return ErrUnavailable
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return ErrUnavailable
	}
	r.retired[id] = true
	m := r.managers[id]
	delete(r.managers, id)
	if m != nil {
		r.retirees.Add(1)
	}
	r.mu.Unlock()
	if m != nil {
		defer r.retirees.Done()
		m.Close()
	}
	return nil
}

func (r *Registry) Close() {
	if r == nil || r.registryState == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		<-r.done
		return
	}
	r.closed = true
	managers := r.managers
	r.managers = map[string]*Manager{}
	r.mu.Unlock()
	for _, m := range managers {
		m.Close()
	}
	r.retirees.Wait()
	close(r.stop)
	<-r.done
}
