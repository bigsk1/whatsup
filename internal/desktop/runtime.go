package desktop

import (
	"errors"

	"whatsup/internal/messaging"
)

// RuntimeOptions is trusted Go construction input, never a bridge DTO. Test
// factories cannot be selected by renderer input or saved peer metadata.
type RuntimeOptions struct {
	AutoConnect        bool
	OnIncoming         func(messaging.Incoming)
	Factories          messaging.Factories
	OnPairingChanged   func(PairingHint)
	OnContactForgotten func(string)
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
	if s.pairingEvents == nil {
		s.pairingEvents = newPairingEvents(options.OnPairingChanged)
	} else {
		s.pairingEvents.setSink(options.OnPairingChanged)
	}
	options.OnPairingChanged = s.pairingEvents.enqueue
	s.runtimeOptions = options
	return nil
}
