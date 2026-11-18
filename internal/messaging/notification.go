package messaging

// Notification never crosses Wails. The native adapter applies separately saved
// opt-ins, length bounds and OS escaping before showing any optional content.
type Notification struct {
	Name string
	Text string
}

func (Notification) String() string               { return "[private WhatsUp notification content]" }
func (Notification) GoString() string             { return "[private WhatsUp notification content]" }
func (Notification) MarshalJSON() ([]byte, error) { return nil, ErrUnavailable }

func (r *Registry) Notification(incoming Incoming) (Notification, error) {
	if r == nil || r.registryState == nil {
		return Notification{}, ErrUnavailable
	}
	r.mu.Lock()
	m := r.managers[incoming.ContactID]
	valid := !r.closed && !r.retired[incoming.ContactID] && m != nil && incoming.origin != nil && incoming.origin == m.managerState
	r.mu.Unlock()
	if !valid {
		return Notification{}, ErrUnavailable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.paused || m.refreshLocked() != nil || m.record.ID != incoming.ContactID || m.conversation == nil || m.state == "failed" {
		return Notification{}, ErrUnavailable
	}
	item, err := m.conversation.GetVisibleIncoming(incoming.MessageID)
	if err != nil {
		return Notification{}, err
	}
	names, err := m.application.ContactNames(incoming.ContactID)
	if err != nil {
		return Notification{}, ErrUnavailable
	}
	name := names.LocalName
	if name == "" {
		name = names.PeerDisplayName
	}
	if name == "" {
		name = "Approved contact"
	}
	return Notification{Name: name, Text: item.Text}, nil
}
