package desktop

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"whatsup/internal/messaging"
)

var ErrNotification = errors.New("Message notification could not be prepared")

type Notification struct {
	Name string
	Body string
}

func (Notification) String() string               { return "[private WhatsUp notification]" }
func (Notification) GoString() string             { return "[private WhatsUp notification]" }
func (Notification) MarshalJSON() ([]byte, error) { return nil, ErrNotification }

func notificationText(value string, maximum int) string {
	if !utf8.ValidString(value) {
		return ""
	}
	var b strings.Builder
	space := false
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = b.Len() > 0
			continue
		}
		if unicode.Is(unicode.Cf, r) && r != 0x200c && r != 0x200d {
			continue
		}
		if space {
			if b.Len()+1 > maximum {
				break
			}
			b.WriteByte(' ')
			space = false
		}
		if b.Len()+utf8.RuneLen(r) > maximum {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// WithIncomingNotification is a Go-only capability boundary. The caller's send
// must be bounded (native notifier uses two seconds). Holding Service.mu through
// delivery serializes it with Go offline, Forget and shutdown. Preferences are
// read before that guard because their API has its own lifecycle lock.
func WithIncomingNotification(s *Service, incoming messaging.Incoming, send func(Notification) error) error {
	if s == nil || send == nil {
		return ErrNotification
	}
	return WithAppPreferences(s, func(prefs AppPreferences) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.state != "unlocked" || s.messages == nil {
			return ErrNotification
		}
		content, err := s.messages.Notification(incoming)
		if err != nil {
			return ErrNotification
		}
		if !prefs.Notifications {
			return nil
		}
		value := Notification{Name: "WhatsUp", Body: "New message"}
		if prefs.NotificationName {
			if name := notificationText(content.Name, 160); name != "" {
				value.Name = name
			}
		}
		if prefs.NotificationPreview {
			if body := notificationText(content.Text, 1024); body != "" {
				value.Body = body
			}
		}
		if send(value) != nil {
			return ErrNotification
		}
		return nil
	})
}
