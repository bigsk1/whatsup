package storage

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedAttentionNeverOffersSetAsideForActiveStagedRoot(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	v, app := openApplicationTest(t, dir, keys)
	defer app.Close()
	if err := app.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(dir, ".recovery-stage-AQEBAQEBAQEBAQEBAQEBAQ")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "state.vault")
	raw := []byte("synthetic damaged active recovery root")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var attention *ManagedCopyError
	policy := managedCopyPolicy{preserveOpaque: true, activeRoot: path}
	if err := preserveManagedRoot(v, path, raw, managedCopyIO{}, policy, ErrCorrupt); !errors.As(err, &attention) || attention.Repairable {
		t.Fatal("active recovery root granted repair", err)
	}
	if err := protectManagedCopiesWithPolicyLocked(v, managedCopyIO{}, policy); !errors.As(err, &attention) || attention.Repairable {
		t.Fatal("protection walk granted repair of active root", err)
	}
}

func TestManagedAttentionSeparatesTargetFromAutomaticRepairCapability(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	v, app := openApplicationTest(t, dir, keys)
	defer app.Close()
	name := "pre-migration-00000000000000000000000000000000.vault"
	for _, fixture := range []struct {
		name  string
		cause error
		size  int
		want  bool
	}{
		{name, ErrCorrupt, 32, true},
		{name, ErrUnsafePath, 32, false},
		{"state.vault", ErrCorrupt, 32, false},
		{".state-123.tmp", ErrCorrupt, 32, false},
		{name, ErrCorrupt, maxManagedRootBytes, false},
	} {
		t.Run(fixture.name+"/"+fixture.cause.Error()+"/"+map[bool]string{true: "repairable", false: "manual"}[fixture.want], func(t *testing.T) {
			path := filepath.Join(dir, fixture.name)
			if err := os.WriteFile(path, make([]byte, fixture.size), 0600); err != nil {
				t.Fatal(err)
			}
			attention := managedCopyError(v, path, fixture.cause).(*ManagedCopyError)
			if attention.Repairable != fixture.want || attention.RelativePath != fixture.name || attention.Digest == [32]byte{} {
				t.Fatal("incorrect repair classification")
			}
		})
	}
	missing := managedCopyError(v, filepath.Join(dir, "pre-migration-11111111111111111111111111111111.vault"), ErrCorrupt).(*ManagedCopyError)
	if missing.Repairable || missing.Digest != [32]byte{} {
		t.Fatal("unreadable target granted repair")
	}
}

func TestManagedSetAsideRequiresFactorsExactCapturedDigestAndHistoricalPath(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	if err := app.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	name := "pre-migration-00000000000000000000000000000000.vault"
	path := filepath.Join(dir, name)
	raw := []byte("WUPVAULT\x02{synthetic interrupted root")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	app.Close()
	for _, bad := range []struct {
		name   string
		secret []byte
		hash   [32]byte
	}{
		{name, []byte("wrong synthetic passphrase"), digest},
		{name, nil, digest},
		{name, testPassphrase, sha256.Sum256([]byte("stale capture"))},
		{"state.vault", testPassphrase, digest},
		{"../" + name, testPassphrase, digest},
	} {
		if err := SetAsideManagedCopy(t.Context(), dir, keys, bad.secret, bad.name, bad.hash); err == nil {
			t.Fatal("invalid repair authority accepted")
		}
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, raw) {
			t.Fatal("failed repair changed original")
		}
	}
	credential := keys.value
	if err := SetAsideManagedCopy(t.Context(), dir, keys, testPassphrase, name, digest); err != nil {
		t.Fatal(err)
	}
	if keys.value != credential {
		t.Fatal("repair replaced OS credential")
	}
	_, app = openProtectedTest(t, dir, keys, testPassphrase)
	sealed, _ := os.ReadFile(path)
	preserved, err := openPreservedRoot(app.vault, path, sealed)
	defer clear(preserved)
	if err != nil || !bytes.Equal(preserved, raw) {
		t.Fatal("repair did not retain exact original bytes", err)
	}
}

func TestConfirmedRecoveryPreservesDamagedRetiringLiveRoot(t *testing.T) {
	f := makeRecoveryFixture(t)
	for _, raw := range [][]byte{[]byte("WUPVAULT\x02{truncated"), []byte("synthetic unrecognized damaged root")} {
		dir, keys := privateDirectory(t), &memoryKeys{}
		_, old := openApplicationTest(t, dir, keys)
		old.Close()
		if err := os.WriteFile(filepath.Join(dir, "state.vault"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		staged, err := StageRecovery(t.Context(), dir, bytes.NewReader(f.archive), testPassphrase)
		if err != nil {
			t.Fatal(err)
		}
		err = staged.Commit(t.Context(), keys, importedAppSecret, true)
		staged.Close()
		if err != nil {
			t.Fatal("confirmed restore rejected damaged retiring root", err)
		}
		_, app := openProtectedTest(t, dir, keys, importedAppSecret)
		assertRestoredFixture(t, app, f)
		paths, _ := managedRootPaths(app.vault)
		found := false
		for _, path := range paths {
			sealed, _ := os.ReadFile(path)
			if bytes.HasPrefix(sealed, preservedRootHeader) {
				plain, err := openPreservedRoot(app.vault, path, sealed)
				if err == nil && bytes.Equal(plain, raw) {
					found = true
				}
				clear(plain)
			}
		}
		if !found {
			t.Fatal("damaged retiring root was not preserved")
		}
	}
}
