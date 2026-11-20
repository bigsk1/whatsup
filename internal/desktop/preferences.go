package desktop

import (
	"errors"
	"sync"

	"whatsup/internal/preferences"
)

type AppPreferences = preferences.Values

type AppPreferenceRuntimeOptions struct {
	SetStartAtLogin func(bool) error
	OnChanged       func(AppPreferences)
}

type appPreferenceState struct {
	mu       sync.Mutex
	mutation sync.Mutex
	store    *preferences.Store
	options  AppPreferenceRuntimeOptions
	closed   bool
}

func ConfigureAppPreferences(s *Service, options AppPreferenceRuntimeOptions) error {
	if s == nil {
		return errors.New("App preferences are unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.preferenceState != nil {
		return errors.New("App preferences are already configured")
	}
	s.preferenceOptions = options
	store, err := preferences.Open(s.directory, s.keys)
	if err != nil {
		return errors.New("App preferences could not be authenticated")
	}
	s.preferenceState = &appPreferenceState{store: store, options: options}
	// Calls stay off unless the authenticated preferences say otherwise.
	if loaded, err := store.Load(); err == nil {
		s.calls.setSettings(loaded.Values)
	} else {
		s.calls.setSettings(AppPreferences{})
	}
	return nil
}

func (s *Service) appPreferences() (*appPreferenceState, error) {
	if s == nil {
		return nil, errors.New("App preferences are unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.preferenceState == nil {
		store, err := preferences.Open(s.directory, s.keys)
		if err != nil {
			return nil, errors.New("App preferences could not be authenticated")
		}
		s.preferenceState = &appPreferenceState{store: store, options: s.preferenceOptions}
		if loaded, err := store.Load(); err == nil {
			s.calls.setSettings(loaded.Values)
		}
	}
	return s.preferenceState, nil
}

func (s *Service) GetAppPreferences() (AppPreferences, error) {
	state, err := s.appPreferences()
	if err != nil {
		return AppPreferences{}, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return AppPreferences{}, errors.New("App preferences are unavailable")
	}
	loaded, err := state.store.Load()
	if err != nil {
		return AppPreferences{}, errors.New("App preferences could not be authenticated")
	}
	return loaded.Values, nil
}

// WithAppPreferences leases current opt-ins through one bounded backend action.
// It runs outside Service.mu; the callback may take that lock, but must not
// mutate preferences or close the preference controller from inside the lease.
// Once SetAppPreferences returns, no earlier lease can still deliver content.
func WithAppPreferences(s *Service, use func(AppPreferences) error) error {
	if use == nil {
		return errors.New("App preferences are unavailable")
	}
	state, err := s.appPreferences()
	if err != nil {
		return err
	}
	state.mutation.Lock()
	defer state.mutation.Unlock()
	state.mu.Lock()
	closed := state.closed
	state.mu.Unlock()
	if closed {
		return errors.New("App preferences are unavailable")
	}
	value, err := state.store.Load()
	if err != nil {
		return errors.New("App preferences could not be authenticated")
	}
	return use(value.Values)
}

func (s *Service) SetAppPreferences(values AppPreferences) (AppPreferences, error) {
	state, err := s.appPreferences()
	if err != nil {
		return AppPreferences{}, err
	}
	state.mutation.Lock()
	defer state.mutation.Unlock()
	state.mu.Lock()
	closed := state.closed
	state.mu.Unlock()
	if closed {
		return AppPreferences{}, errors.New("App preferences are unavailable")
	}
	old, err := state.store.Load()
	if err != nil {
		return AppPreferences{}, errors.New("App preferences could not be authenticated")
	}
	if values == old.Values {
		return old.Values, nil
	}
	changedStartup := old.Values.StartAtLogin != values.StartAtLogin
	if changedStartup {
		if state.options.SetStartAtLogin == nil || state.options.SetStartAtLogin(values.StartAtLogin) != nil {
			return old.Values, errors.New("Start at login could not be changed")
		}
	}
	next := old
	next.Values = values
	if err := state.store.Save(next); err != nil {
		if changedStartup && state.options.SetStartAtLogin(old.Values.StartAtLogin) != nil {
			return old.Values, errors.New("App preferences could not be saved or startup restored. Review your operating system startup setting")
		}
		return old.Values, errors.New("App preferences could not be saved")
	}
	if state.options.OnChanged != nil {
		state.options.OnChanged(values)
	}
	// A call setting takes effect at once, including on a call in progress.
	if old.Values.VoiceCalls != values.VoiceCalls || old.Values.VideoCalls != values.VideoCalls {
		s.applyCallSettings(values)
	}
	return values, nil
}

func TrayNoticeSeen(s *Service) (bool, error) {
	state, err := s.appPreferences()
	if err != nil {
		return false, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return false, errors.New("App preferences are unavailable")
	}
	value, err := state.store.Load()
	if err != nil {
		return false, errors.New("App preferences could not be authenticated")
	}
	return value.TrayNoticeSeen, nil
}

func MarkTrayNoticeSeen(s *Service) error {
	state, err := s.appPreferences()
	if err != nil {
		return err
	}
	state.mutation.Lock()
	defer state.mutation.Unlock()
	state.mu.Lock()
	closed := state.closed
	state.mu.Unlock()
	if closed {
		return errors.New("App preferences are unavailable")
	}
	value, err := state.store.Load()
	if err != nil {
		return errors.New("App preferences could not be authenticated")
	}
	if value.TrayNoticeSeen {
		return nil
	}
	value.TrayNoticeSeen = true
	if state.store.Save(value) != nil {
		return errors.New("The tray notice could not be saved")
	}
	return nil
}

func CloseAppPreferences(s *Service) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	state := s.preferenceState
	if state == nil {
		s.preferenceState = &appPreferenceState{closed: true}
	}
	s.mu.Unlock()
	if state == nil {
		return nil
	}
	state.mutation.Lock()
	defer state.mutation.Unlock()
	state.mu.Lock()
	state.closed = true
	state.mu.Unlock()
	if state.store != nil && state.store.Close() != nil {
		return errors.New("App preferences could not be closed")
	}
	return nil
}

// ReopenAppPreferences is Go-only recovery integration. It retains native
// callbacks and is never an ordinary unlock fallback for unreadable preferences.
func ReopenAppPreferences(s *Service) error {
	if s == nil {
		return errors.New("App preferences are unavailable")
	}
	if err := CloseAppPreferences(s); err != nil {
		return err
	}
	s.mu.Lock()
	options := s.preferenceOptions
	s.preferenceState = nil
	s.mu.Unlock()
	return ConfigureAppPreferences(s, options)
}
