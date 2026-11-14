package preferences

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

type testKeys struct {
	value string
	err   error
}

func (k *testKeys) Get() (string, error) {
	if k.err != nil {
		return "", k.err
	}
	if k.value == "" {
		return "", keyring.ErrNotFound
	}
	return k.value, nil
}

func testStore(t *testing.T) (*Store, *testKeys, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "private")
	keys := &testKeys{value: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{73}, 32))}
	store, err := Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, keys, directory
}

func TestFreshDefaultsDoNotCreateCredentialOrPreferenceFile(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	store, err := Open(directory, &testKeys{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state, err := store.Load()
	if err != nil || state != (Snapshot{Values: Defaults()}) {
		t.Fatal("fresh preferences did not use private defaults")
	}
	if err := store.Save(state); !errors.Is(err, ErrCredential) {
		t.Fatal("preferences persisted before OS credential existed")
	}
	if _, err := os.Stat(filepath.Join(directory, Filename)); !os.IsNotExist(err) {
		t.Fatal("default read created a preference file")
	}
}

func TestEncryptedBooleanPreferencesSurviveReopenWithFreshNonces(t *testing.T) {
	store, keys, directory := testStore(t)
	state := Snapshot{Values: Values{CloseToTray: true, Notifications: true, NotificationName: true}, TrayNoticeSeen: true}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(directory, Filename))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(directory, Filename))
	if bytes.Equal(first, second) || bytes.Equal(first[len(header):len(header)+24], second[len(header):len(header)+24]) {
		t.Fatal("preference encryption reused a nonce")
	}
	for _, sentinel := range []string{"closeToTray", "notifications", "notificationName", "trayNoticeSeen", keys.value} {
		if bytes.Contains(second, []byte(sentinel)) {
			t.Fatal("device preference state appeared outside ciphertext")
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory, keys)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	got, err := store.Load()
	if err != nil || got != state {
		t.Fatal("encrypted boolean preferences were not restored")
	}
}

func TestSurvivingPrivateStateCannotUseMissingOrReplacedCredential(t *testing.T) {
	for _, name := range []string{Filename, "state.vault", "messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal"} {
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "private")
			if err := os.MkdirAll(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, name), []byte{1, 2, 3}, 0600); err != nil {
				t.Fatal(err)
			}
			if store, err := Open(directory, &testKeys{}); !errors.Is(err, ErrCredential) {
				if store != nil {
					_ = store.Close()
				}
				t.Fatal("surviving state accepted absent wrapping credential")
			}
		})
	}
	store, keys, directory := testStore(t)
	if err := store.Save(Snapshot{Values: Defaults()}); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(filepath.Join(directory, Filename))
	keys.value = ""
	if err := store.Save(Snapshot{}); !errors.Is(err, ErrCredential) {
		t.Fatal("lost key was replaced or ignored")
	}
	unchanged, _ := os.ReadFile(filepath.Join(directory, Filename))
	if !bytes.Equal(original, unchanged) {
		t.Fatal("missing credential changed existing preferences")
	}
}

func TestTamperedPreferencesAndWrongKeyFailClosed(t *testing.T) {
	for _, change := range []string{"cipher", "truncated", "oversized", "wrong-key", "header"} {
		t.Run(change, func(t *testing.T) {
			store, keys, directory := testStore(t)
			if err := store.Save(Snapshot{Values: Defaults()}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, Filename)
			wire, _ := os.ReadFile(path)
			switch change {
			case "cipher":
				wire[len(wire)-1] ^= 1
			case "truncated":
				wire = wire[:len(wire)-1]
			case "oversized":
				wire = make([]byte, maxFileBytes+1)
			case "wrong-key":
				keys.value = base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{74}, 32))
			case "header":
				wire[0] ^= 1
			}
			if err := os.WriteFile(path, wire, 0600); err != nil {
				t.Fatal(err)
			}
			if opened, err := Open(directory, keys); !errors.Is(err, ErrCorrupt) {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatal("tampered preferences were accepted or replaced")
			}
		})
	}
}

func TestInterruptedPreferenceSavePreservesCiphertextAndCleansTemporaryFile(t *testing.T) {
	store, _, directory := testStore(t)
	if err := store.Save(Snapshot{Values: Defaults()}); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(filepath.Join(directory, Filename))
	store.beforeWrite = func() error { return errors.New("synthetic write boundary") }
	if err := store.Save(Snapshot{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal("interrupted save reported success")
	}
	got, _ := os.ReadFile(filepath.Join(directory, Filename))
	if !bytes.Equal(original, got) {
		t.Fatal("interrupted write replaced the previous ciphertext")
	}
	temporary, err := filepath.Glob(filepath.Join(directory, ".preferences-*.tmp"))
	if err != nil || len(temporary) != 0 {
		t.Fatal("interrupted preference write left temporary files")
	}
}

func TestPreferenceLockAndPrivatePathsAreRequired(t *testing.T) {
	store, keys, directory := testStore(t)
	if second, err := Open(directory, keys); !errors.Is(err, ErrBusy) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatal("second preference writer acquired the lock")
	}
	if err := store.Save(Snapshot{Values: Defaults()}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, Filename)
	if err := os.Rename(path, path+".synthetic-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{1}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrUnsafePath) {
		t.Fatal("replaced preference inode was accepted")
	}
	if runtime.GOOS != "windows" {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0755); err != nil {
			t.Fatal(err)
		}
		if opened, err := Open(directory, keys); !errors.Is(err, ErrUnsafePath) {
			if opened != nil {
				_ = opened.Close()
			}
			t.Fatal("non-private preference directory was accepted")
		}
	}
}

func TestPreferenceCapabilitiesAreValueSafeAndClosedTogether(t *testing.T) {
	store, _, _ := testStore(t)
	for _, value := range []any{store, *store, Store{}, &Store{}} {
		for _, verb := range []string{"%v", "%+v", "%#v"} {
			if fmt.Sprintf(verb, value) != "[private WhatsUp device preferences]" {
				t.Fatal("preference capability formatting exposed backend state")
			}
		}
		if _, err := json.Marshal(value); err == nil {
			t.Fatal("preference capability was serializable")
		}
	}
	alias := *store
	if err := alias.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrUnavailable) {
		t.Fatal("closing copy did not retire original capability")
	}
	var zero Store
	if _, err := zero.Load(); !errors.Is(err, ErrUnavailable) {
		t.Fatal("zero preference capability could read state")
	}
}
