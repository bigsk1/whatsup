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
	// OnCallCapture opens or closes the native microphone gate. It is called
	// synchronously, with true only while a call has been answered, before the
	// interface can learn of that state.
	OnCallCapture func(bool)
	// OnIncomingCall asks the native host to bring the window forward once
	// for each new ringing call. It must return promptly.
	OnIncomingCall func()
	// OnCallChanged tells the interface to re-read one contact's call view.
	OnCallChanged func(CallHint)
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
	s.callOptionsMu.Lock()
	s.callRuntimeOptions = options
	s.callOptionsMu.Unlock()
	return nil
}
