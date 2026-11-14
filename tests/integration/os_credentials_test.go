//go:build oscredentials

package integration

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
	"whatsup/internal/storage"
)

type validationOSKeys struct{ account string }

func (k validationOSKeys) Get() (string, error) {
	return keyring.Get("WhatsUp Development Validation", k.account)
}
func (k validationOSKeys) Set(value string) error {
	return keyring.Set("WhatsUp Development Validation", k.account, value)
}

// Explicit opt-in: writes one disposable OS credential under a separate service,
// checks actual provider persistence, and removes that credential after the test.
// Never uses the application's production identity credential or prints secrets.
func TestRealOSCredentialProtectedVault(t *testing.T) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal("synthetic account generation failed")
	}
	keys := validationOSKeys{account: hex.EncodeToString(suffix[:])}
	credentialDeleted := false
	t.Cleanup(func() {
		if credentialDeleted {
			return
		}
		if err := keyring.Delete("WhatsUp Development Validation", keys.account); err != nil {
			t.Error("disposable OS credential cleanup failed")
		}
	})
	dir := filepath.Join(t.TempDir(), "private")
	vault, err := storage.Open(dir, keys)
	if err != nil {
		t.Fatal("real OS credential provider unavailable")
	}
	t.Cleanup(func() { _ = vault.Close() })
	want := []byte("synthetic OS credential provider validation")
	if err := vault.Save(want); err != nil {
		t.Fatal("synthetic vault save failed")
	}
	if err := vault.Close(); err != nil {
		t.Fatal("synthetic vault close failed")
	}
	vault, err = storage.Open(dir, keys)
	if err != nil {
		t.Fatal("real OS credential reopen failed")
	}
	got, err := vault.Load()
	defer clear(got)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("real provider roundtrip failed")
	}
	if err := vault.Close(); err != nil {
		t.Fatal("synthetic vault close failed")
	}
	if err := keyring.Delete("WhatsUp Development Validation", keys.account); err != nil {
		t.Fatal("disposable OS credential removal failed")
	}
	credentialDeleted = true
	if reopened, err := storage.Open(dir, keys); err == nil {
		_ = reopened.Close()
		t.Fatal("existing vault reopened without its OS credential")
	}
	if _, err := keys.Get(); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatal("missing credential was replaced or provider did not report missing")
	}
}
