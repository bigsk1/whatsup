package desktop

import (
	"errors"

	"whatsup/internal/messaging"
)

// RuntimeOptions is trusted Go construction input, never a bridge DTO. Test
// factories cannot be selected by renderer input or saved peer metadata.
type RuntimeOptions struct {
	AutoConnect bool
	OnIncoming  func(messaging.Incoming)
	Factories   messaging.Factories
}

func ConfigureRuntime(s *Service, options RuntimeOptions) error {
	if s == nil {
		return errors.New("Desktop runtime is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "locked" || s.vault != nil || s.messages != nil {
		return errors.New("Desktop runtime is already active")
	}
	s.runtimeOptions = options
	return nil
}
