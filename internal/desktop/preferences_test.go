package desktop

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"whatsup/internal/preferences"
)

type preferenceTestKeys struct {
	mu    sync.Mutex
	value string
	fail  bool
}

func (k *preferenceTestKeys) Get() (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail {
		return "", errors.New("synthetic private OS failure")
	}
	if k.value == "" {
		return "", keyring.ErrNotFound
	}
	return k.value, nil
}
func (k *preferenceTestKeys) Set(value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.value = value
	return nil
}

func configuredPreferences(t *testing.T, options AppPreferenceRuntimeOptions) (*Service, *preferenceTestKeys) {
	t.Helper()
	keys := &preferenceTestKeys{value: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{82}, 32))}
	s := New(filepath.Join(t.TempDir(), "private"), keys)
	if err := ConfigureAppPreferences(s, options); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.ServiceShutdown() })
	return s, keys
}

func TestAppPreferenceDefaultsAndPrivateNoticeStayAvailableWhileLocked(t *testing.T) {
	s, _ := configuredPreferences(t, AppPreferenceRuntimeOptions{})
	got, err := s.GetAppPreferences()
	if err != nil || got != preferences.Defaults() || s.GetStatus().StorageState != "locked" {
		t.Fatal("locked preference defaults were unavailable")
	}
	if seen, err := TrayNoticeSeen(s); err != nil || seen {
		t.Fatal("fresh tray notice was already consumed")
	}
	if err := MarkTrayNoticeSeen(s); err != nil {
		t.Fatal(err)
	}
	next := got
	next.Notifications = false
	if result, err := s.SetAppPreferences(next); err != nil || result != next {
		t.Fatal("boolean preferences could not be saved while locked")
	}
	if seen, err := TrayNoticeSeen(s); err != nil || !seen {
		t.Fatal("public preference save reset private tray notice")
	}
	wire, err := json.Marshal(next)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	// Five original settings plus the three call settings, and nothing private.
	if json.Unmarshal(wire, &fields) != nil || len(fields) != 8 || fields["trayNoticeSeen"] != nil {
		t.Fatal("preference DTO exposed non-public internal fields")
	}
	if fields["voiceCalls"] != false || fields["callRingMuted"] != false || fields["videoCalls"] != false {
		t.Fatal("call settings are missing from the preference DTO or on by default")
	}
	if err := CloseAppPreferences(s); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAppPreferences(); err == nil {
		t.Fatal("closed preference controller reopened itself")
	}
}

func TestPreferenceCallbacksPermitServiceAndPreferenceReads(t *testing.T) {
	var s *Service
	var calls []bool
	changed := make(chan AppPreferences, 1)
	s, _ = configuredPreferences(t, AppPreferenceRuntimeOptions{
		SetStartAtLogin: func(value bool) error {
			_ = s.GetStatus()
			if _, err := s.GetAppPreferences(); err != nil {
				return err
			}
			calls = append(calls, value)
			return nil
		},
		OnChanged: func(value AppPreferences) {
			_ = s.GetStatus()
			got, err := s.GetAppPreferences()
			if err == nil && got == value {
				changed <- value
			}
		},
	})
	next := preferences.Defaults()
	next.StartAtLogin, next.NotificationPreview = true, true
	done := make(chan error, 1)
	go func() { _, err := s.SetAppPreferences(next); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native callback deadlocked behind service/preference read lock")
	}
	if !reflect.DeepEqual(calls, []bool{true}) {
		t.Fatal("startup callback did not reflect explicit opt-in")
	}
	select {
	case got := <-changed:
		if got != next {
			t.Fatal("post-commit callback received stale preferences")
		}
	default:
		t.Fatal("committed preference notification was not delivered")
	}
}

func TestStartupSideEffectRollsBackWhenEncryptedSaveFails(t *testing.T) {
	var keys *preferenceTestKeys
	var calls []bool
	s, loadedKeys := configuredPreferences(t, AppPreferenceRuntimeOptions{SetStartAtLogin: func(value bool) error {
		calls = append(calls, value)
		keys.mu.Lock()
		keys.fail = value // fail the OS-key read during Save, then allow rollback
		keys.mu.Unlock()
		return nil
	}})
	keys = loadedKeys
	next := preferences.Defaults()
	next.StartAtLogin = true
	result, err := s.SetAppPreferences(next)
	if err == nil || result != preferences.Defaults() || !reflect.DeepEqual(calls, []bool{true, false}) {
		t.Fatal("failed encrypted preference save did not roll back OS startup")
	}
	if strings.Contains(err.Error(), "synthetic private OS") {
		t.Fatal("raw provider failure crossed the bridge")
	}
	if got, err := s.GetAppPreferences(); err != nil || got != preferences.Defaults() {
		t.Fatal("failed save replaced the existing preference state")
	}
}

func TestFailedStartupChangeCannotPersistOtherBooleanChanges(t *testing.T) {
	s, _ := configuredPreferences(t, AppPreferenceRuntimeOptions{SetStartAtLogin: func(bool) error { return errors.New("synthetic native failure") }})
	next := preferences.Defaults()
	next.StartAtLogin, next.NotificationName = true, true
	if _, err := s.SetAppPreferences(next); err == nil {
		t.Fatal("failed OS startup change reported success")
	}
	if got, err := s.GetAppPreferences(); err != nil || got != preferences.Defaults() {
		t.Fatal("failed startup change persisted a partial boolean update")
	}
}

func TestPreferenceShutdownWithoutReadsCannotAcquireNewWriter(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "private"), &preferenceTestKeys{})
	if err := CloseAppPreferences(s); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetAppPreferences(); err == nil {
		t.Fatal("late read created preferences after shutdown")
	}
}

func TestPreferenceOptOutWaitsForPriorBoundedDeliveryLease(t *testing.T) {
	s, _ := configuredPreferences(t, AppPreferenceRuntimeOptions{})
	enabled := preferences.Defaults()
	enabled.NotificationPreview = true
	if _, err := s.SetAppPreferences(enabled); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- WithAppPreferences(s, func(value AppPreferences) error {
			if !value.NotificationPreview {
				return errors.New("enabled preview lease was lost")
			}
			_ = s.GetStatus() // The lease does not hold Service.mu.
			if _, err := s.GetAppPreferences(); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("preference lease could not read service/preference state")
	}
	started, saved := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		_, err := s.SetAppPreferences(preferences.Defaults())
		saved <- err
	}()
	<-started
	select {
	case <-saved:
		close(release)
		t.Fatal("opt-out returned while an older preview delivery was still live")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	if err := <-leaseDone; err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if err := WithAppPreferences(s, func(value AppPreferences) error {
		if value.NotificationPreview {
			return errors.New("new delivery retained an opted-out preview")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
