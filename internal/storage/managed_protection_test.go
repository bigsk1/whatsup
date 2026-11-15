package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var managedSecretB = []byte("synthetic replacement secret B")

func managedCopiesFixture(t *testing.T, dir string, v *Vault, root []byte) []string {
	t.Helper()
	container := filepath.Join(dir, ".recovery-rollback-AQEBAQEBAQEBAQEBAQEBAQ")
	if err := os.Mkdir(container, 0700); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(container, "state.vault"), filepath.Join(container, "pre-migration-ffffffffffffffffffffffffffffffff.vault")}
	for i := range 5 {
		paths = append(paths, filepath.Join(dir, fmt.Sprintf("pre-migration-%032x.vault", i)))
	}
	for _, path := range paths {
		if err := os.WriteFile(path, root, 0600); err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(path) == dir {
			if err := recordManagedBackup(v, filepath.Base(path), root); err != nil {
				t.Fatal(err)
			}
		}
	}
	return paths
}

func assertManagedCopyProtection(t *testing.T, app *ApplicationState, paths []string, current, rejected []byte) {
	t.Helper()
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		} // Retention may remove a top-level snapshot.
		if err != nil {
			t.Fatal(err)
		}
		if rejected != nil {
			scratch := scratchVault(app.vault)
			plain, err := decodeRootWithSecret(scratch, data, rejected)
			clear(plain)
			clearScratch(scratch)
			if !errors.Is(err, ErrPassphrase) {
				t.Fatal("a retained copy still accepts the old secret")
			}
		}
		scratch := scratchVault(app.vault)
		plain, err := decodeRootWithSecret(scratch, data, current)
		clear(plain)
		same := bytes.Equal(scratch.master, app.vault.master)
		clearScratch(scratch)
		if err != nil || !same {
			t.Fatal("retained copy lost its payload/master or current protection", err)
		}
		root, err := decodeRoot(data[len(rootHeader):])
		if err != nil || (root.Mode == "os-plus-passphrase") != (len(current) != 0) {
			t.Fatal("retained copy mode mismatch", err)
		}
	}
}

func TestManagedCopiesChangeAndDisableFollowCommittedProtection(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	root, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	paths := managedCopiesFixture(t, dir, app.vault, root)
	if err := app.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	if err := app.SetPassphrase(t.Context(), testPassphrase, managedSecretB); err != nil {
		t.Fatal(err)
	}
	assertManagedCopyProtection(t, app, paths, managedSecretB, testPassphrase)
	app.Close()
	_, app = openProtectedTest(t, dir, keys, managedSecretB)
	if err := app.SetPassphrase(t.Context(), managedSecretB, nil); err != nil {
		t.Fatal(err)
	}
	assertManagedCopyProtection(t, app, paths, nil, nil)
	app.Close()
	_, app = openApplicationTest(t, dir, keys)
	assertManagedCopyProtection(t, app, paths, nil, nil)
}

func TestCommittedPassphraseChangeSurvivesRetentionFailure(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(fmt.Sprint(disable), func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			_, app := openApplicationTest(t, dir, keys)
			if err := app.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
				t.Fatal(err)
			}
			root, err := os.ReadFile(filepath.Join(dir, "state.vault"))
			if err != nil {
				t.Fatal(err)
			}
			paths := managedCopiesFixture(t, dir, app.vault, root)
			next := managedSecretB
			if disable {
				next = nil
			}
			attempted := false
			err = app.setPassphraseWithIO(t.Context(), testPassphrase, next, managedCopyIO{remove: func(string) error { attempted = true; return os.ErrPermission }})
			if err != nil || !attempted {
				t.Fatal("committed change reported cleanup failure", err)
			}
			if _, err := app.Profile(); err != nil {
				t.Fatal("cleanup poisoned healthy app", err)
			}
			assertManagedCopyProtection(t, app, paths, next, nil)
			app.Close()
			if disable {
				_, app = openApplicationTest(t, dir, keys)
			} else {
				_, app = openProtectedTest(t, dir, keys, next)
			}
			assertManagedCopyProtection(t, app, paths, next, nil)
		})
	}
}

func TestDamagedManagedBackupHasSpecificFailClosedError(t *testing.T) {
	for _, damage := range []string{"truncated", "os-slot-intact", "current-protected-payload"} {
		t.Run(damage, func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			_, app := openApplicationTest(t, dir, keys)
			original, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
			if err := app.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
				t.Fatal(err)
			}
			if damage == "current-protected-payload" {
				original, _ = os.ReadFile(filepath.Join(dir, "state.vault"))
			}
			if damage == "truncated" {
				original = original[:len(rootHeader)+12]
			} else {
				root, err := decodeRoot(original[len(rootHeader):])
				if err != nil {
					t.Fatal(err)
				}
				root.Payload.Ciphertext[0] ^= 1
				encoded, _ := json.Marshal(root)
				original = append(bytes.Clone(rootHeader), encoded...)
			}
			name := "pre-migration-00000000000000000000000000000000.vault"
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			app.Close()
			v, err := OpenWithPassphrase(dir, keys, testPassphrase)
			if err != nil {
				t.Fatal(err)
			}
			_, err = OpenApplication(v)
			var attention *ManagedCopyError
			if !errors.As(err, &attention) || attention.RelativePath != name {
				t.Fatal("damaged backup did not identify required repair", err)
			}
			v.Close()
			retained, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(retained, original) {
				t.Fatal("damaged backup lost or modified")
			}
			// Explicitly setting aside synthetic damaged material restores access.
			if err := os.Rename(path, filepath.Join(t.TempDir(), "preserved.vault")); err != nil {
				t.Fatal(err)
			}
			_, app = openProtectedTest(t, dir, keys, testPassphrase)
			if _, err := app.Identity(); err != nil {
				t.Fatal("healthy live state lost", err)
			}
		})
	}
}

func TestDamagedOldSecretBackupCannotSurviveSuccessfulProtectionChange(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	if err := app.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
	root, err := decodeRoot(data[len(rootHeader):])
	if err != nil {
		t.Fatal(err)
	}
	root.Payload.Ciphertext[0] ^= 1
	encoded, _ := json.Marshal(root)
	data = append(bytes.Clone(rootHeader), encoded...)
	path := filepath.Join(dir, "pre-migration-00000000000000000000000000000000.vault")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var attention *ManagedCopyError
	if err := app.SetPassphrase(t.Context(), testPassphrase, managedSecretB); !errors.As(err, &attention) {
		t.Fatal("damaged A wrapper survived successful A to B", err)
	}
	app.Close()
	// Repair is explicit; the failed transition has not replaced the live root.
	if err := os.Rename(path, filepath.Join(t.TempDir(), "preserved.vault")); err != nil {
		t.Fatal(err)
	}
	_, app = openProtectedTest(t, dir, keys, testPassphrase)
	if err := app.SetPassphrase(t.Context(), testPassphrase, managedSecretB); err != nil {
		t.Fatal(err)
	}
	// A historical salt cannot make the same damaged copy silently ignorable.
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	app.Close()
	v, err := OpenWithPassphrase(dir, keys, managedSecretB)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := OpenApplication(v); !errors.As(err, &attention) {
		t.Fatal("damaged historical wrapper was skipped on reopen", err)
	}
}

func TestPendingRecoverySecurelyPreservesDamagedManagedCopy(t *testing.T) {
	f := makeRecoveryFixture(t)
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	app.Close()
	staged, err := StageRecovery(t.Context(), dir, bytes.NewReader(f.archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	staged.testStep = func(step string) error {
		if step == "journal" {
			return errors.New("synthetic interrupted recovery")
		}
		return nil
	}
	if err := staged.Commit(t.Context(), keys, testPassphrase, true); err == nil {
		t.Fatal("recovery did not stop after journal")
	}
	staged.Close()
	path := filepath.Join(dir, "pre-migration-00000000000000000000000000000000.vault")
	if err := os.WriteFile(path, []byte("WUPVAULT\x02{synthetic damaged copy"), 0600); err != nil {
		t.Fatal(err)
	}
	_, app = openProtectedTest(t, dir, keys, testPassphrase)
	assertRestoredFixture(t, app, f)
	sealed, err := readCipherFile(path, maxManagedRootBytes)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := openPreservedRoot(app.vault, path, sealed)
	defer clear(plain)
	if err != nil || !bytes.Equal(plain, []byte("WUPVAULT\x02{synthetic damaged copy")) {
		t.Fatal("interrupted recovery did not preserve damaged copy", err)
	}
}

func TestManagedProtectionCrashHelper(t *testing.T) {
	dir := os.Getenv("WHATSUP_MANAGED_CRASH_TARGET")
	if dir == "" {
		t.Skip("isolated process-death helper")
	}
	keys := &memoryKeys{value: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{193}, 32))}
	mode, boundary := os.Getenv("WHATSUP_MANAGED_CRASH_MODE"), os.Getenv("WHATSUP_MANAGED_CRASH_BOUNDARY")
	current, next := testPassphrase, managedSecretB
	if mode == "enable" {
		current, next = nil, testPassphrase
	}
	if mode == "disable" {
		next = nil
	}
	v, err := OpenWithPassphrase(dir, keys, current)
	if err != nil {
		t.Fatal(err)
	}
	app, err := OpenApplication(v)
	if err != nil {
		t.Fatal(err)
	}
	// Opening legitimately prunes older snapshots. Re-create excess synthetic
	// snapshots so the post-commit cleanup boundary is exercised in this child.
	if boundary == "committed" {
		root, err := os.ReadFile(filepath.Join(dir, "state.vault"))
		if err != nil {
			t.Fatal(err)
		}
		for i := range 5 {
			name := fmt.Sprintf("pre-migration-%032x.vault", i)
			if err := os.WriteFile(filepath.Join(dir, name), root, 0600); err != nil {
				t.Fatal(err)
			}
			if err := recordManagedBackup(v, name, root); err != nil {
				t.Fatal(err)
			}
		}
	}
	operations := managedCopyIO{replace: func(path string, data []byte) error {
		if err := replaceManagedRoot(path, data); err != nil {
			return err
		}
		if boundary == "rewrap" {
			os.Exit(91)
		}
		return nil
	}, remove: func(path string) error {
		if boundary == "committed" {
			os.Exit(91)
		}
		return os.Remove(path)
	}}
	if err := app.setPassphraseWithIO(context.Background(), current, next, operations); err != nil {
		t.Fatal(err)
	}
	t.Fatal("did not reach crash boundary")
}

func TestManagedProtectionActualProcessCrashReopensCommittedMode(t *testing.T) {
	for _, mode := range []string{"enable", "change", "disable"} {
		for _, boundary := range []string{"rewrap", "committed"} {
			t.Run(mode+"/"+boundary, func(t *testing.T) {
				dir := privateDirectory(t)
				keys := &memoryKeys{value: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{193}, 32))}
				_, app := openApplicationTest(t, dir, keys)
				if mode != "enable" {
					if err := app.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
						t.Fatal(err)
					}
				}
				root, err := os.ReadFile(filepath.Join(dir, "state.vault"))
				if err != nil {
					t.Fatal(err)
				}
				paths := managedCopiesFixture(t, dir, app.vault, root)
				identity, _ := app.Identity()
				seed := identity.Seed()
				app.Close()
				cmd := exec.Command(os.Args[0], "-test.run=^TestManagedProtectionCrashHelper$")
				cmd.Env = append(os.Environ(), "WHATSUP_MANAGED_CRASH_TARGET="+dir, "WHATSUP_MANAGED_CRASH_MODE="+mode, "WHATSUP_MANAGED_CRASH_BOUNDARY="+boundary)
				output, err := cmd.CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 91 {
					t.Fatalf("crash boundary missed: %v %s", err, output)
				}
				secret := testPassphrase
				if mode == "enable" && boundary == "rewrap" || mode == "disable" {
					secret = nil
				}
				if mode == "change" && boundary == "committed" {
					secret = managedSecretB
				}
				if len(secret) == 0 {
					_, app = openApplicationTest(t, dir, keys)
				} else {
					_, app = openProtectedTest(t, dir, keys, secret)
				}
				identity, _ = app.Identity()
				if identity.Seed() != seed {
					t.Fatal("crash changed identity")
				}
				assertManagedCopyProtection(t, app, paths, secret, nil)
			})
		}
	}
}
