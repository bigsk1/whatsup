package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var testPassphrase = []byte("  synthetic passphrase exact spaces  ")

func openProtectedTest(t *testing.T, dir string, keys KeyStore, secret []byte) (*Vault, *ApplicationState) {
	t.Helper()
	v, err := OpenWithPassphrase(dir, keys, secret)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	a, err := OpenApplication(v)
	if err != nil {
		t.Fatal(err)
	}
	return v, a
}

func TestPassphraseTwoFactorsPersistThroughEveryRootSaveAndDisable(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	v, a := openApplicationTest(t, dir, keys)
	master := bytes.Clone(v.master)
	defer clear(master)
	identity, _ := a.Identity()
	seed := identity.Seed()
	credential := keys.value
	record, _, _, _, chat, _ := prepareForgetStorage(t, a, 141)
	if err := a.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetProfile("Synthetic protected name", true); err != nil {
		t.Fatal(err)
	}
	if err := a.SetContactName(record.ID, "Synthetic label"); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.PendingIDs(); err != nil {
		t.Fatal("mode change retired healthy chat scope")
	}
	if !bytes.Equal(master, v.master) || keys.value != credential || keys.writes != 1 {
		t.Fatal("protection changed hierarchy or credential")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := InspectProtection(dir)
	if err != nil || !info.RequiresPassphrase {
		t.Fatal("protected startup hint missing")
	}
	if _, err := OpenWithPassphrase(dir, keys, nil); !errors.Is(err, ErrPassphraseRequired) {
		t.Fatal("OS factor alone opened protected vault")
	}
	bad, err := OpenWithPassphrase(dir, keys, []byte("synthetic wrong password"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenApplication(bad); !errors.Is(err, ErrPassphrase) {
		t.Fatal("wrong password authenticated")
	}
	bad.Close()
	v, a = openProtectedTest(t, dir, keys, testPassphrase)
	identity, _ = a.Identity()
	if identity.Seed() != seed || !bytes.Equal(v.master, master) {
		t.Fatal("protected reopen changed identity/master")
	}
	if err := a.SetPassphrase(context.Background(), []byte("synthetic wrong password"), nil); !errors.Is(err, ErrPassphrase) {
		t.Fatal("wrong current password disabled")
	}
	if _, err := a.Profile(); err != nil {
		t.Fatal("wrong secret poisoned healthy app")
	}
	if err := a.SetPassphrase(context.Background(), testPassphrase, nil); err != nil {
		t.Fatal(err)
	}
	a.Close()
	_, a = openApplicationTest(t, dir, keys)
	identity, _ = a.Identity()
	if identity.Seed() != seed {
		t.Fatal("disable changed identity")
	}
	info, err = InspectProtection(dir)
	if err != nil || info.RequiresPassphrase {
		t.Fatal("disable did not persist")
	}
}

func TestPassphraseManagedOSBackupsHaveNoMasterBypass(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, a := openApplicationTest(t, dir, keys)
	original, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "pre-migration-0123456789abcdef0123456789abcdef.vault")
	if os.WriteFile(backup, original, 0600) != nil {
		t.Fatal("backup")
	}
	rollback := filepath.Join(dir, ".recovery-rollback-AQEBAQEBAQEBAQEBAQEBAQ")
	if os.Mkdir(rollback, 0700) != nil {
		t.Fatal("rollback")
	}
	if os.WriteFile(filepath.Join(rollback, "state.vault"), original, 0600) != nil {
		t.Fatal("rollback root")
	}
	if err := a.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{backup, filepath.Join(rollback, "state.vault")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		scratch := scratchVault(a.vault)
		if _, err := scratch.loadRootLocked(data[len(rootHeader):]); !errors.Is(err, ErrPassphraseRequired) {
			t.Fatal("backup retained OS-only access")
		}
		plain, err := decodeRootWithSecret(scratch, data, testPassphrase)
		clear(plain)
		clearScratch(scratch)
		if err != nil {
			t.Fatal("backup was not rewrapped", err)
		}
	}
}

func TestPassphraseTransitionUncertainSaveRequiresReopen(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			_, a := openApplicationTest(t, dir, keys)
			save := a.save
			a.save = func(data []byte) error {
				if after {
					if err := save(data); err != nil {
						return err
					}
				}
				return errors.New("synthetic failed transition")
			}
			if err := a.SetPassphrase(context.Background(), nil, testPassphrase); err == nil {
				t.Fatal("uncertain save reported success")
			}
			if _, err := a.SetProfile("late", false); !errors.Is(err, ErrStateFailed) {
				t.Fatal("failed transition retained writer")
			}
			a.Close()
			if after {
				_, _ = openProtectedTest(t, dir, keys, testPassphrase)
			} else {
				_, _ = openApplicationTest(t, dir, keys)
			}
		})
	}
}

func TestPassphraseReenableRewrapsSameMasterHistoricalSecrets(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	secretB := []byte("synthetic second local passphrase")
	if err := app.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	protectedA, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "pre-migration-0123456789abcdef0123456789abcdef.vault")
	if os.WriteFile(backup, protectedA, 0600) != nil {
		t.Fatal("backup")
	}
	if err := app.SetPassphrase(context.Background(), testPassphrase, nil); err != nil {
		t.Fatal(err)
	}
	if err := app.SetPassphrase(context.Background(), nil, secretB); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	scratch := scratchVault(app.vault)
	if _, err := decodeRootWithSecret(scratch, data, testPassphrase); !errors.Is(err, ErrPassphrase) {
		t.Fatal("historical secret A still exposed current master")
	}
	clearScratch(scratch)
	scratch = scratchVault(app.vault)
	plain, err := decodeRootWithSecret(scratch, data, secretB)
	clear(plain)
	clearScratch(scratch)
	if err != nil {
		t.Fatal("historical same-master root did not use secret B", err)
	}
}

func TestLegacyBackupMasterRotatesAcrossFailedAndDisabledProtection(t *testing.T) {
	for _, flow := range []string{"disable", "change", "failed-enable"} {
		t.Run(flow, func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			_, app := openApplicationTest(t, dir, keys)
			payload, err := json.Marshal(app.state)
			if err != nil {
				t.Fatal(err)
			}
			defer clear(payload)
			aead, err := app.vault.aead()
			if err != nil {
				t.Fatal(err)
			}
			nonce := make([]byte, aead.NonceSize())
			rand.Read(nonce)
			data := append(bytes.Clone(header), nonce...)
			data = aead.Seal(data, nonce, payload, header)
			backup := filepath.Join(dir, "pre-migration-0123456789abcdef0123456789abcdef.vault")
			if os.WriteFile(backup, data, 0600) != nil {
				t.Fatal("legacy backup")
			}
			if flow == "failed-enable" {
				app.save = func([]byte) error { return errors.New("synthetic root commit interruption") }
			}
			err = app.SetPassphrase(context.Background(), nil, testPassphrase)
			if flow == "failed-enable" {
				if err == nil {
					t.Fatal("fault absent")
				}
				app.Close()
				_, app = openApplicationTest(t, dir, keys)
			} else if err != nil {
				t.Fatal(err)
			}
			protected, err := os.ReadFile(backup)
			if err != nil {
				t.Fatal(err)
			}
			root, err := decodeRoot(protected[len(rootHeader):])
			if err != nil || root.ManagedMaster == nil {
				t.Fatal("legacy master has no authenticated current-master capsule")
			}
			current := testPassphrase
			if flow == "disable" {
				if err := app.SetPassphrase(context.Background(), testPassphrase, nil); err != nil {
					t.Fatal(err)
				}
				current = nil
			}
			if flow == "failed-enable" {
				current = nil
			}
			secretB := []byte("synthetic later secret B value")
			if err := app.SetPassphrase(context.Background(), current, secretB); err != nil {
				t.Fatal(err)
			}
			protected, err = os.ReadFile(backup)
			if err != nil {
				t.Fatal(err)
			}
			scratch := scratchVault(app.vault)
			if _, err := decodeRootWithSecret(scratch, protected, testPassphrase); !errors.Is(err, ErrPassphrase) {
				t.Fatal("legacy identity backup retained secret A")
			}
			clearScratch(scratch)
			scratch = scratchVault(app.vault)
			plain, err := decodeRootWithSecret(scratch, protected, secretB)
			if err != nil || !bytes.Equal(plain, payload) {
				t.Fatal("legacy backup payload changed or B failed")
			}
			clear(plain)
			clearScratch(scratch)
		})
	}
}

func TestPassphraseCleansOnlyOwnedUncommittedRootTemporaryFiles(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	root, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".state-12345.tmp", ".protection-67890.tmp"} {
		if os.WriteFile(filepath.Join(dir, name), root, 0600) != nil {
			t.Fatal("root temporary")
		}
	}
	unrelated := filepath.Join(dir, ".state-owner-note.tmp")
	if os.WriteFile(unrelated, []byte("synthetic unrelated note"), 0600) != nil {
		t.Fatal("unrelated")
	}
	if err := app.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".state-12345.tmp", ".protection-67890.tmp"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatal("owned temporary root retained master bypass")
		}
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("unrelated path removed")
	}
}
