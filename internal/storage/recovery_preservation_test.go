package storage

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

var importedAppSecret = []byte("synthetic new recovery app secret")

func TestForeignProtectedRecoveryPreservesOpaqueRootsAndReopens(t *testing.T) {
	f := makeRecoveryFixture(t)
	for _, protected := range []bool{true, false} {
		t.Run(map[bool]string{true: "protected", false: "os-only-then-enable"}[protected], func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			_, old := openApplicationTest(t, dir, keys)
			if err := old.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
				t.Fatal(err)
			}
			oldMaster := bytes.Clone(old.vault.master)
			defer clear(oldMaster)
			oldRoot, _ := os.ReadFile(filepath.Join(dir, "state.vault"))
			credential := keys.value
			old.Close()
			secret := importedAppSecret
			if !protected {
				secret = nil
			}
			for restore := range 2 {
				staged, err := StageRecovery(t.Context(), dir, bytes.NewReader(f.archive), testPassphrase)
				if err != nil {
					t.Fatal(err)
				}
				err = staged.Commit(t.Context(), keys, secret, true)
				staged.Close()
				if err != nil {
					t.Fatal("different protected profile replacement failed", err)
				}
				if keys.value != credential {
					t.Fatal("replacement changed existing OS credential")
				}
				var app *ApplicationState
				if protected {
					_, app = openProtectedTest(t, dir, keys, secret)
				} else {
					_, app = openApplicationTest(t, dir, keys)
				}
				assertRestoredFixture(t, app, f)
				paths, err := managedRootPaths(app.vault)
				if err != nil {
					t.Fatal(err)
				}
				preserved := 0
				for _, path := range paths {
					data, err := readCipherFile(path, maxManagedRootBytes)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.HasPrefix(data, preservedRootHeader) {
						continue
					}
					plain, err := openPreservedRoot(app.vault, path, data)
					if err != nil {
						t.Fatal("new authority cannot authenticate preserved root", err)
					}
					if restore == 0 && !bytes.Equal(plain, oldRoot) {
						t.Fatal("preservation changed historical root bytes")
					}
					clear(plain)
					oldAuthority := &Vault{&vaultState{master: oldMaster}}
					if plain, err := openPreservedRoot(oldAuthority, path, data); err == nil {
						clear(plain)
						t.Fatal("historical master opened imported protection")
					}
					preserved++
				}
				if preserved == 0 {
					t.Fatal("opaque rollback root was not preserved")
				}
				app.Close()
			}
			if !protected {
				_, app := openApplicationTest(t, dir, keys)
				if err := app.SetPassphrase(t.Context(), nil, importedAppSecret); err != nil {
					t.Fatal("opaque recovery prevents later protection", err)
				}
				app.Close()
			}
			_, app := openProtectedTest(t, dir, keys, importedAppSecret)
			assertRestoredFixture(t, app, f)
			app.Close()
		})
	}
}

func TestPreservedRootRoleBindingAndCorruptionFailClosed(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	if err := app.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pre-migration-00000000000000000000000000000000.vault")
	data := []byte("synthetic opaque damaged backup bytes")
	sealed, err := sealPreservedRoot(app.vault, path, data)
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := openPreservedRoot(app.vault, filepath.Join(dir, "state.vault"), sealed); err == nil {
		clear(plain)
		t.Fatal("preserved bytes accepted under another root role")
	}
	if err := os.WriteFile(path, sealed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.SetPassphrase(t.Context(), testPassphrase, importedAppSecret); err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(path)
	if !bytes.Equal(installed, sealed) {
		t.Fatal("factor change needlessly rewrote master-protected material")
	}
	installed[len(installed)-1] ^= 1
	if err := os.WriteFile(path, installed, 0600); err != nil {
		t.Fatal(err)
	}
	app.Close()
	v, err := OpenWithPassphrase(dir, keys, importedAppSecret)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	var attention *ManagedCopyError
	if _, err := OpenApplication(v); !errors.As(err, &attention) {
		t.Fatal("corrupt preservation wrapper was silently skipped", err)
	}
}

func TestForeignRecoveryCrashHelper(t *testing.T) {
	dir := os.Getenv("WHATSUP_FOREIGN_CRASH_TARGET")
	if dir == "" {
		t.Skip("isolated process crash helper")
	}
	archive, err := os.ReadFile(os.Getenv("WHATSUP_FOREIGN_CRASH_ARCHIVE"))
	if err != nil {
		t.Fatal(err)
	}
	keys := &memoryKeys{value: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{207}, 32))}
	staged, err := StageRecovery(t.Context(), dir, bytes.NewReader(archive), testPassphrase)
	if err != nil {
		t.Fatal(err)
	}
	staged.testStep = func(step string) error {
		if step == os.Getenv("WHATSUP_FOREIGN_CRASH_STEP") {
			os.Exit(77)
		}
		return nil
	}
	if err := staged.Commit(context.Background(), keys, importedAppSecret, true); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash boundary not reached")
}

func TestForeignProtectedRecoveryActualCrashReconciles(t *testing.T) {
	f := makeRecoveryFixture(t)
	archive := filepath.Join(t.TempDir(), "synthetic-recovery.wup")
	if err := os.WriteFile(archive, f.archive, 0600); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"protected", "state.vault"} {
		t.Run(step, func(t *testing.T) {
			dir := privateDirectory(t)
			keys := &memoryKeys{value: base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{207}, 32))}
			_, old := openApplicationTest(t, dir, keys)
			if err := old.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
				t.Fatal(err)
			}
			old.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestForeignRecoveryCrashHelper$")
			cmd.Env = append(os.Environ(), "WHATSUP_FOREIGN_CRASH_TARGET="+dir, "WHATSUP_FOREIGN_CRASH_ARCHIVE="+archive, "WHATSUP_FOREIGN_CRASH_STEP="+step)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 {
				t.Fatalf("foreign restore crash did not stop: %v %s", err, output)
			}
			_, app := openProtectedTest(t, dir, keys, importedAppSecret)
			assertRestoredFixture(t, app, f)
		})
	}
}
