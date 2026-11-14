package chat

import (
	"crypto/sha256"
	"errors"
	"sort"
	"sync"

	"whatsup/internal/message"
)

// memoryStore is a transactional dependency fixture. Encryption, physical disk
// failures, WAL contents and crash recovery are tested by the real repository.
type memoryStore struct {
	mu              sync.Mutex
	rows            map[string]Message
	hidden          map[string]bool
	pending         map[string]bool
	digests         map[string][32]byte
	next            uint64
	used            uint64
	quota           uint64
	writes          int
	reads           int
	failOn          string
	failAfter       bool
	pendingOverride []string
}

func (s *memoryStore) init() {
	if s.rows == nil {
		s.rows = make(map[string]Message)
		s.hidden = make(map[string]bool)
		s.pending = make(map[string]bool)
		s.digests = make(map[string][32]byte)
	}
	if s.quota == 0 {
		s.quota = 1 << 30
	}
}

func (s *memoryStore) fault(operation string, after bool) error {
	if s.failOn == operation && s.failAfter == after {
		return errors.New("synthetic uncertain repository operation")
	}
	return nil
}

func (s *memoryStore) Queue(item Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if err := s.fault("queue", false); err != nil {
		return err
	}
	if _, exists := s.rows[item.ID]; exists {
		return message.ErrConflict
	}
	cost := uint64(128 + len(item.Text))
	if s.used+cost > s.quota {
		return message.ErrCapacity
	}
	s.next++
	item.Sequence = s.next
	s.rows[item.ID], s.pending[item.ID] = item, true
	s.digests[item.ID] = sha256.Sum256([]byte(item.Text))
	s.used += cost
	s.writes++
	return s.fault("queue", true)
}

func (s *memoryStore) Receive(item Message) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if err := s.fault("receive", false); err != nil {
		return false, err
	}
	if prior, exists := s.rows[item.ID]; exists {
		if prior.Direction != Incoming || s.digests[item.ID] != sha256.Sum256([]byte(item.Text)) {
			return false, message.ErrConflict
		}
		return true, nil
	}
	cost := uint64(128 + len(item.Text))
	if s.used+cost > s.quota {
		return false, message.ErrCapacity
	}
	s.next++
	item.Sequence, item.Direction, item.Delivered = s.next, Incoming, true
	s.rows[item.ID] = item
	s.digests[item.ID] = sha256.Sum256([]byte(item.Text))
	s.used += cost
	s.writes++
	return false, s.fault("receive", true)
}

func (s *memoryStore) Acknowledge(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if err := s.fault("ack", false); err != nil {
		return err
	}
	item, exists := s.rows[id]
	if !exists || item.Direction != Outgoing {
		return message.ErrAck
	}
	if item.Delivered {
		return nil
	}
	item.Delivered = true
	delete(s.pending, id)
	if s.hidden[id] {
		s.used -= uint64(len(item.Text))
		item.Text = ""
	}
	s.rows[id] = item
	s.writes++
	return s.fault("ack", true)
}

func (s *memoryStore) PendingIDs() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.reads++
	if err := s.fault("pending", false); err != nil {
		return nil, err
	}
	if s.pendingOverride != nil {
		return append([]string(nil), s.pendingOverride...), nil
	}
	var items []Message
	for id := range s.pending {
		items = append(items, s.rows[id])
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids, nil
}

func (s *memoryStore) Get(id string) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.reads++
	if err := s.fault("get", false); err != nil {
		return Message{}, err
	}
	item, exists := s.rows[id]
	if !exists || s.hidden[id] && !s.pending[id] {
		return Message{}, message.ErrNotFound
	}
	return item, nil
}

func (s *memoryStore) Page(before uint64, limit int) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.reads++
	if err := s.fault("page", false); err != nil {
		return nil, err
	}
	var items []Message
	for id, item := range s.rows {
		if !s.hidden[id] && (before == 0 || item.Sequence < before) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
	if len(items) > limit {
		items = items[len(items)-limit:]
	}
	return items, nil
}

func (s *memoryStore) Usage() (message.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.reads++
	if err := s.fault("usage", false); err != nil {
		return message.Usage{}, err
	}
	return message.Usage{UsedBytes: s.used, QuotaBytes: s.quota, Full: s.used+129 > s.quota}, nil
}

func (s *memoryStore) hide(id string) {
	item := s.rows[id]
	s.hidden[id] = true
	if !s.pending[id] {
		s.used -= uint64(len(item.Text))
		item.Text = ""
		s.rows[id] = item
	}
}

func (s *memoryStore) DeleteLocal(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if err := s.fault("delete", false); err != nil {
		return err
	}
	if _, exists := s.rows[id]; !exists {
		return message.ErrNotFound
	}
	s.hide(id)
	s.writes++
	return s.fault("delete", true)
}

func (s *memoryStore) ClearHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if err := s.fault("clear", false); err != nil {
		return err
	}
	for id := range s.rows {
		s.hide(id)
	}
	s.writes++
	return s.fault("clear", true)
}
