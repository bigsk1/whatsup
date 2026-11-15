package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOSOnlyRetentionKeepsThreeReadableTopLevelSnapshots(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	root, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i := range 6 {
		path := filepath.Join(dir, fmt.Sprintf("pre-migration-%032x.vault", i))
		if err := os.WriteFile(path, root, 0600); err != nil {
			t.Fatal(err)
		}
		if err := recordManagedBackup(app.vault, filepath.Base(path), root); err != nil {
			t.Fatal(err)
		}
		// Reversed, attacker-mutable timestamps must not reverse creation order.
		at := time.Unix(int64(1000-i), 0)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	if err := app.finishManagedCopiesLocked(); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		want := i >= 3
		if want && err != nil || !want && !os.IsNotExist(err) {
			t.Fatalf("OS-only retention incorrect for snapshot %d", i)
		}
	}
}

func TestRetentionDoesNotCountUnreadableRootsOrPruneRollbackContents(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	root, err := os.ReadFile(filepath.Join(dir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	readable := filepath.Join(dir, "pre-migration-00000000000000000000000000000000.vault")
	if err := os.WriteFile(readable, root, 0600); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(1, 0)
	if err := os.Chtimes(readable, at, at); err != nil {
		t.Fatal(err)
	}
	otherDir, otherKeys := privateDirectory(t), &memoryKeys{}
	_, other := openApplicationTest(t, otherDir, otherKeys)
	unreadable, err := os.ReadFile(filepath.Join(otherDir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		path := filepath.Join(dir, fmt.Sprintf("pre-migration-%032x.vault", i+1))
		if err := os.WriteFile(path, unreadable, 0600); err != nil {
			t.Fatal(err)
		}
	}
	other.Close()
	container := filepath.Join(dir, ".recovery-rollback-AQEBAQEBAQEBAQEBAQEBAQ")
	if err := os.Mkdir(container, 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(container, "pre-migration-ffffffffffffffffffffffffffffffff.vault")
	if err := os.WriteFile(nested, root, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(nested, at, at); err != nil {
		t.Fatal(err)
	}
	if err := app.SetPassphrase(t.Context(), nil, testPassphrase); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{readable, nested} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retention deleted recoverable copy %s: %v", filepath.Base(path), err)
		}
	}
}
