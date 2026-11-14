package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

type memoryKeys struct {
	value   string
	failure error
	writes  int
}

func (m *memoryKeys) Get() (string, error) {
	if m.failure != nil {
		return "", m.failure
	}
	if m.value == "" {
		return "", keyring.ErrNotFound
	}
	return m.value, nil
}
func (m *memoryKeys) Set(value string) error {
	m.writes++
	if m.failure != nil {
		return m.failure
	}
	m.value = value
	return nil
}

func openTest(t *testing.T, dir string, keys *memoryKeys) *Vault {
	t.Helper()
	v, err := Open(dir, keys)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close() })
	return v
}

func privateDirectory(t *testing.T) string {
	t.Helper()
	// Go's testing.TempDir child may use ordinary 0777/umask permissions. Let
	// storage create its own explicitly private directory inside that test root.
	return filepath.Join(t.TempDir(), "private")
}

func TestEncryptedStateSurvivesReopenAndNeverWritesPlaintext(t *testing.T) {
	dir := privateDirectory(t)
	keys := &memoryKeys{}
	v := openTest(t, dir, keys)
	plain := []byte("synthetic private identity and unsent content")
	if err := v.Save(plain); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
	if bytes.Contains(first, plain) {
		t.Fatal("plaintext persisted")
	}
	if err := v.Save(plain); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
	if bytes.Equal(first, second) {
		t.Fatal("nonce reused")
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	v = openTest(t, dir, keys)
	got, err := v.Load()
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("reopen failed: %v", err)
	}
	if keys.writes != 1 {
		t.Fatal("credential replaced")
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.Name() != "state.vault" && entry.Name() != "state.lock" {
			t.Fatal("unexpected file retained")
		}
	}
}

func TestCredentialFailureCannotReplaceExistingState(t *testing.T) {
	dir := privateDirectory(t)
	keys := &memoryKeys{}
	v := openTest(t, dir, keys)
	if err := v.Save([]byte("synthetic")); err != nil {
		t.Fatal(err)
	}
	_ = v.Close()
	for _, failed := range []*memoryKeys{{}, {failure: errors.New("secret service absent")}, {value: "invalid"}} {
		if _, err := Open(dir, failed); !errors.Is(err, ErrCredential) {
			t.Fatalf("must fail closed: %v", err)
		}
		if failed.writes != 0 {
			t.Fatal("replaced key for existing vault")
		}
	}
	v = openTest(t, dir, keys)
	if _, err := v.Load(); err != nil {
		t.Fatal("original state changed")
	}
}

func TestTamperingTruncationAndWrongKeyAreRejected(t *testing.T) {
	dir := privateDirectory(t)
	keys := &memoryKeys{}
	v := openTest(t, dir, keys)
	_ = v.Save([]byte("synthetic"))
	filename := filepath.Join(dir, "state.vault")
	original, _ := os.ReadFile(filename)
	for _, offset := range []int{0, len(header), len(original) - 1} {
		changed := bytes.Clone(original)
		changed[offset] ^= 1
		_ = os.WriteFile(filename, changed, 0600)
		if _, err := v.Load(); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("accepted changed byte %d: %v", offset, err)
		}
	}
	_ = os.WriteFile(filename, original[:10], 0600)
	if _, err := v.Load(); !errors.Is(err, ErrCorrupt) {
		t.Fatal("accepted truncation")
	}
	_ = os.WriteFile(filename, original, 0600)
	_ = v.Close()
	other := &memoryKeys{}
	u := openTest(t, privateDirectory(t), other)
	_ = u.Close()
	v = openTest(t, dir, other)
	if _, err := v.Load(); !errors.Is(err, ErrCorrupt) {
		t.Fatal("accepted wrong key")
	}
}

func TestBoundsLocksAndUnsafeFiles(t *testing.T) {
	dir := privateDirectory(t)
	keys := &memoryKeys{}
	v := openTest(t, dir, keys)
	if _, err := Open(dir, keys); !errors.Is(err, ErrBusy) {
		t.Fatalf("second process lock: %v", err)
	}
	if err := v.Save(make([]byte, MaxStateBytes+1)); err == nil {
		t.Fatal("accepted oversized state")
	}
	_ = v.Close()
	if err := v.Save(nil); !errors.Is(err, ErrLocked) {
		t.Fatal("closed vault accessible")
	}
	if _, err := v.Load(); !errors.Is(err, ErrLocked) {
		t.Fatal("closed vault readable")
	}
	if runtime.GOOS != "windows" {
		publicDirectory := privateDirectory(t)
		if err := os.Mkdir(publicDirectory, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(publicDirectory, keys); !errors.Is(err, ErrUnsafePath) {
			t.Fatal("accepted nonprivate directory")
		}
		unsafe := filepath.Join(t.TempDir(), "state.vault")
		if err := os.Symlink(unsafe, filepath.Join(dir, "state.vault")); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir, keys); !errors.Is(err, ErrUnsafePath) {
			t.Fatal("accepted symlink")
		}
	}
}

func TestVaultFormattingAndJSONCannotExposeSyntheticKeyMaterial(t *testing.T) {
	v := &Vault{vaultState: &vaultState{key: []byte("synthetic OS wrapping material"), master: []byte("synthetic unwrapped master material")}}
	for _, value := range []any{v, *v, Vault{}, &Vault{}} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if got := fmt.Sprintf(verb, value); got != "[locked backend-only WhatsUp vault]" {
				t.Fatal("vault value or pointer formatting exposed its state")
			}
		}
		if data, err := json.Marshal(value); err == nil || len(data) != 0 {
			t.Fatal("vault value or pointer was serializable")
		}
	}
}

func TestZeroVaultsFailClosedAndCopiesShareRevocation(t *testing.T) {
	for _, v := range []*Vault{nil, {}} {
		if _, err := OpenApplication(v); !errors.Is(err, ErrLocked) {
			t.Fatal("zero vault constructed application authority")
		}
		if _, err := v.Load(); !errors.Is(err, ErrLocked) {
			t.Fatal("zero vault returned readable state")
		}
		if err := v.Save([]byte("synthetic forbidden state")); !errors.Is(err, ErrLocked) {
			t.Fatal("zero vault saved data")
		}
		if _, err := v.deriveKeyLocked("messages/v1"); !errors.Is(err, ErrLocked) {
			t.Fatal("zero vault derived a key")
		}
		if err := v.Close(); err != nil {
			t.Fatal("closing an empty capability was not idempotent")
		}
	}
	v := openTest(t, privateDirectory(t), &memoryKeys{})
	if err := v.Save([]byte("synthetic shared capability")); err != nil {
		t.Fatal(err)
	}
	copy := *v
	if err := copy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Load(); !errors.Is(err, ErrLocked) {
		t.Fatal("closing a copied vault left its original capability active")
	}
	if err := v.Save(nil); !errors.Is(err, ErrLocked) {
		t.Fatal("copied vault closure allowed its original to write")
	}
}
