package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestProtectedReopenRepairsNestedBackupsAndTemporaryRoots(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	original, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	identity, _ := app.Identity()
	seed := identity.Seed()
	if err := app.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	var paths, temps, unrelated []string
	for _, name := range []string{".recovery-stage-AQEBAQEBAQEBAQEBAQEBAQ", ".recovery-rollback-AgICAgICAgICAgICAgICAg"} {
		container := filepath.Join(dir, name)
		if err := os.Mkdir(container, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(container, "pre-migration-0123456789abcdef0123456789abcdef.vault")
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
		for _, temp := range []string{".state-12345.tmp", ".protection-67890.tmp"} {
			path := filepath.Join(container, temp)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			temps = append(temps, path)
		}
		path = filepath.Join(container, ".state-user-note.tmp")
		if err := os.WriteFile(path, []byte("synthetic unrelated note"), 0600); err != nil {
			t.Fatal(err)
		}
		unrelated = append(unrelated, path)
	}
	app.Close()
	_, app = openProtectedTest(t, dir, keys, testPassphrase)
	secretB := []byte("synthetic replacement passphrase B")
	for _, secret := range [][]byte{testPassphrase, secretB} {
		if bytes.Equal(secret, secretB) {
			if err := app.SetPassphrase(context.Background(), testPassphrase, secretB); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			scratch := scratchVault(app.vault)
			plain, err := decodeRootWithSecret(scratch, data, nil)
			clear(plain)
			clearScratch(scratch)
			if !errors.Is(err, ErrPassphraseRequired) {
				t.Fatal("reopen left an OS-only managed root")
			}
			scratch = scratchVault(app.vault)
			plain, err = decodeRootWithSecret(scratch, data, secret)
			clear(plain)
			same := bytes.Equal(scratch.master, app.vault.master)
			clearScratch(scratch)
			if err != nil || !same {
				t.Fatal("managed root did not follow authenticated protection change")
			}
		}
	}
	for _, path := range temps {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("nested crash temporary retained an OS-only root")
		}
	}
	for _, path := range unrelated {
		if data, err := os.ReadFile(path); err != nil || string(data) != "synthetic unrelated note" {
			t.Fatal("unrelated nested file was changed")
		}
	}
	identity, _ = app.Identity()
	if identity.Seed() != seed {
		t.Fatal("repair changed identity")
	}
}

func TestManagedMigrationRetentionKeepsNewestThreeAndPinnedCopy(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	original, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	container := filepath.Join(dir, ".recovery-rollback-AQEBAQEBAQEBAQEBAQEBAQ")
	if err := os.Mkdir(container, 0700); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i := range 12 {
		parent := dir
		if i%2 != 0 {
			parent = container
		}
		path := filepath.Join(parent, fmt.Sprintf("pre-migration-%032x.vault", i))
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		at := time.Unix(int64(1000+i), 0)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if err := app.SetPassphrase(context.Background(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		want := i%2 != 0 || i >= len(paths)-2*maxRetainedMigrationBackups
		if want && err != nil || !want && !os.IsNotExist(err) {
			t.Fatalf("first protection lost original snapshot age at index %d", i)
		}
	}
	// Recreate authenticated managed backups with deterministic ages, then keep
	// the oldest top-level snapshot pinned by the current envelope.
	protected, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
	for i, path := range paths {
		if err := os.WriteFile(path, protected, 0600); err != nil {
			t.Fatal(err)
		}
		at := time.Unix(int64(1000+i), 0)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	app.state.MigrationBackup = filepath.Base(paths[0])
	if err := app.finishManagedCopiesLocked(); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		want := i == 0 || i%2 != 0 || i >= len(paths)-2*maxRetainedMigrationBackups
		if want && err != nil || !want && !os.IsNotExist(err) {
			t.Fatalf("retention violated pinned/newest policy at index %d", i)
		}
	}
	if _, err := os.Stat(container); err != nil {
		t.Fatal("retention removed a recovery container")
	}
}
