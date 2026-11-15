package storage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func publishCatalogTestBackup(t *testing.T, app *ApplicationState, index int, record bool) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(app.vault.directory, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("pre-migration-%032x.vault", index)
	if err := os.WriteFile(filepath.Join(app.vault.directory, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	if record {
		if err := recordManagedBackup(app.vault, name, data); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(app.vault.directory, name)
}

func TestManagedCatalogIgnoresMutableTimesAndPreservesUnlistedCopy(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	paths := make([]string, 6)
	for i := range paths {
		paths[i] = publishCatalogTestBackup(t, app, i, true)
		at := time.Unix(int64(100-i), 0)
		if os.Chtimes(paths[i], at, at) != nil {
			t.Fatal("fixture timestamp change failed")
		}
	}
	unlisted := publishCatalogTestBackup(t, app, 99, false)
	if err := app.pruneManagedCopiesLocked(managedCopyIO{}); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		if i < 3 && !os.IsNotExist(err) || i >= 3 && err != nil {
			t.Fatalf("mutable time influenced retention at %d", i)
		}
	}
	if _, err := os.Stat(unlisted); err != nil {
		t.Fatal("publication interrupted before catalog entry was deleted")
	}
	catalog, err := loadManagedCatalog(app.vault)
	if err != nil || len(catalog.Entries) != 3 || catalog.NextSequence != 7 {
		t.Fatal("catalog lost monotonic creation order")
	}
}

func TestManagedCatalogTamperOrWrongMasterNeverPrunesOrOverwrites(t *testing.T) {
	for _, mode := range []string{"tamper", "other-master"} {
		t.Run(mode, func(t *testing.T) {
			dir, keys := privateDirectory(t), &memoryKeys{}
			_, app := openApplicationTest(t, dir, keys)
			var paths []string
			for i := range 6 {
				paths = append(paths, publishCatalogTestBackup(t, app, i, true))
			}
			path := filepath.Join(dir, managedCatalogFilename)
			wire, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(wire, []byte(filepath.Base(paths[0]))) || bytes.Contains(wire, []byte("sequence")) {
				t.Fatal("catalog retained readable backup metadata")
			}
			if mode == "tamper" {
				wire[len(wire)-1] ^= 1
			} else {
				otherDir, otherKeys := privateDirectory(t), &memoryKeys{}
				_, other := openApplicationTest(t, otherDir, otherKeys)
				publishCatalogTestBackup(t, other, 0, true)
				wire, err = os.ReadFile(filepath.Join(otherDir, managedCatalogFilename))
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, wire, 0600); err != nil {
				t.Fatal(err)
			}
			if app.pruneManagedCopiesLocked(managedCopyIO{}) == nil {
				t.Fatal("unverified catalog treated as retention authority")
			}
			for _, backup := range paths {
				if _, err := os.Stat(backup); err != nil {
					t.Fatal("unverified catalog deleted a backup")
				}
			}
			// A newly published root remains usable/unlisted; optional catalog
			// maintenance cannot turn a successful migration into a failed one.
			if err := app.backupRootMetadataLocked(); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, wire) {
				t.Fatal("unverified catalog silently overwritten")
			}
			app.Close()
			_, reopened := openApplicationTest(t, dir, keys)
			if _, err := reopened.Identity(); err != nil {
				t.Fatal("optional catalog corruption blocked healthy app")
			}
		})
	}
}

func TestManagedCatalogDigestMismatchDoesNotDisplaceReadableBackups(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	paths := make([]string, 8)
	for i := range paths {
		paths[i] = publishCatalogTestBackup(t, app, i, true)
	}
	otherDir, otherKeys := privateDirectory(t), &memoryKeys{}
	_, _ = openApplicationTest(t, otherDir, otherKeys)
	foreign, err := os.ReadFile(filepath.Join(otherDir, "state.vault"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[7], foreign, 0600); err != nil {
		t.Fatal(err)
	}
	if recordManagedBackup(app.vault, filepath.Base(paths[7]), foreign) == nil {
		t.Fatal("different master acquired creation-order authority")
	}
	if err := os.WriteFile(paths[6], []byte("damaged synthetic backup"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.pruneManagedCopiesLocked(managedCopyIO{}); err != nil {
		t.Fatal(err)
	}
	for i, path := range paths {
		_, err := os.Stat(path)
		if i < 3 && !os.IsNotExist(err) || i >= 3 && err != nil {
			t.Fatalf("unreadable/swapped copy displaced a readable snapshot at %d", i)
		}
	}
}

func TestManagedCatalogRewrapPreservesSequenceAndUpdatesExactDigest(t *testing.T) {
	dir, keys := privateDirectory(t), &memoryKeys{}
	_, app := openApplicationTest(t, dir, keys)
	path := publishCatalogTestBackup(t, app, 1, true)
	before, err := loadManagedCatalog(app.vault)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	root, err := decodeRoot(data[len(rootHeader):])
	if err != nil {
		t.Fatal(err)
	}
	plain, err := managedPayload(root, app.vault.master)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	sealed, err := app.vault.sealRootLocked(plain)
	if err != nil {
		t.Fatal(err)
	}
	if err := replaceManagedRootTracked(app.vault, path, sealed, managedCopyIO{}); err != nil {
		t.Fatal(err)
	}
	after, err := loadManagedCatalog(app.vault)
	if err != nil || after.NextSequence != before.NextSequence || after.Entries[0].Sequence != before.Entries[0].Sequence || after.Entries[0].Digest != managedDigest(sealed) || after.Entries[0].Digest == before.Entries[0].Digest {
		t.Fatal("rewrap discarded sequence or failed to authenticate new ciphertext")
	}
}

func TestManagedCatalogRejectsInvalidSequenceAndPathMetadata(t *testing.T) {
	valid := managedCatalog{Version: 1, NextSequence: 2, Entries: []managedCatalogEntry{{Name: "pre-migration-00000000000000000000000000000001.vault", Digest: managedDigest([]byte("synthetic")), Sequence: 1}}}
	if !validManagedCatalog(valid) {
		t.Fatal("legitimate catalog refused")
	}
	for _, change := range []func(*managedCatalog){
		func(c *managedCatalog) { c.Version = 2 },
		func(c *managedCatalog) { c.NextSequence = 0 },
		func(c *managedCatalog) { c.Entries[0].Name = "../state.vault" },
		func(c *managedCatalog) { c.Entries[0].Sequence = 2 },
		func(c *managedCatalog) { c.Entries[0].Sequence = 0 },
		func(c *managedCatalog) { c.Entries = append(c.Entries, c.Entries[0]) },
	} {
		candidate := valid
		candidate.Entries = append([]managedCatalogEntry(nil), valid.Entries...)
		change(&candidate)
		if validManagedCatalog(candidate) {
			t.Fatal("invalid order/path metadata accepted")
		}
	}
}
