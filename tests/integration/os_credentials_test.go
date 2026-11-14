//go:build oscredentials

package integration

import (
	"bytes"
	"context"
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

func TestRealOSCredentialPassphraseAndRecovery(t *testing.T) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal("synthetic account generation failed")
	}
	keys := validationOSKeys{account: hex.EncodeToString(suffix[:])}
	t.Cleanup(func() {
		if err := keyring.Delete("WhatsUp Development Validation", keys.account); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			t.Error("disposable OS credential cleanup failed")
		}
	})
	dir := filepath.Join(t.TempDir(), "private")
	secret := []byte("  synthetic OS protected recovery secret  ")
	defer clear(secret)
	vault, err := storage.OpenWithPassphrase(dir, keys, nil)
	if err != nil {
		t.Fatal("real OS credential provider unavailable")
	}
	t.Cleanup(func() { _ = vault.Close() })
	app, err := storage.OpenApplication(vault)
	if err != nil {
		t.Fatal("synthetic application creation failed")
	}
	local, err := app.Identity()
	if err != nil {
		t.Fatal("synthetic identity unavailable")
	}
	want := local.PublicKey()
	if err := app.SetPassphrase(context.Background(), nil, secret); err != nil {
		t.Fatal("real OS factor could not enable app protection")
	}
	var archive bytes.Buffer
	if err := app.ExportRecovery(context.Background(), &archive, secret); err != nil {
		t.Fatal("protected synthetic recovery export failed")
	}
	defer clear(archive.Bytes())
	if err := vault.Close(); err != nil {
		t.Fatal("protected synthetic vault close failed")
	}
	if v, err := storage.OpenWithPassphrase(dir, keys, nil); !errors.Is(err, storage.ErrPassphraseRequired) {
		if v != nil {
			_ = v.Close()
		}
		t.Fatal("real OS factor bypassed required app passphrase")
	}
	vault, err = storage.OpenWithPassphrase(dir, keys, secret)
	if err != nil {
		t.Fatal("protected synthetic vault reopen failed")
	}
	app, err = storage.OpenApplication(vault)
	if err != nil {
		t.Fatal("protected synthetic state authentication failed")
	}
	got, err := app.Identity()
	if err != nil || !got.PublicKey().Equal(want) {
		t.Fatal("protected synthetic identity changed")
	}
	_ = vault.Close()
	if err := keyring.Delete("WhatsUp Development Validation", keys.account); err != nil {
		t.Fatal("disposable credential removal failed")
	}
	if v, err := storage.OpenWithPassphrase(dir, keys, secret); err == nil {
		_ = v.Close()
		t.Fatal("app passphrase bypassed missing real OS factor")
	}
	staged, err := storage.StageRecovery(context.Background(), dir, bytes.NewReader(archive.Bytes()), secret)
	if err != nil {
		t.Fatal("synthetic recovery could not authenticate after credential loss")
	}
	defer staged.Close()
	preview, err := staged.InspectTarget(keys)
	if err != nil || !preview.ReplaceRequired || !preview.CredentialResetRequired {
		t.Fatal("lost real OS credential was not surfaced for explicit reset")
	}
	if err := staged.Commit(context.Background(), keys, secret, true); err != nil {
		t.Fatal("authenticated recovery could not replace the missing real OS factor")
	}
	vault, err = storage.OpenWithPassphrase(dir, keys, secret)
	if err != nil {
		t.Fatal("restored real OS factor could not reopen")
	}
	app, err = storage.OpenApplication(vault)
	if err != nil {
		t.Fatal("restored synthetic application could not authenticate")
	}
	got, err = app.Identity()
	if err != nil || !got.PublicKey().Equal(want) {
		t.Fatal("recovery changed the approved synthetic identity")
	}
}
