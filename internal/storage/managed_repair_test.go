package storage

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
)

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
