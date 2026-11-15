package storage

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
)

const managedCatalogFilename = "managed-backups.vault"
const maxManagedCatalogEntries = 1024
const maxManagedCatalogBytes = 256 << 10

var managedCatalogHeader = []byte("WUPBACKUPCAT\x01")

type managedCatalogEntry struct {
	Name     string `json:"name"`
	Digest   string `json:"sha256"`
	Sequence uint64 `json:"sequence"`
}
type managedCatalog struct {
	Version      int                   `json:"version"`
	NextSequence uint64                `json:"nextSequence"`
	Entries      []managedCatalogEntry `json:"entries"`
}
type managedCatalogState struct {
	managedCatalog
	exists   bool
	observed [sha256.Size]byte
}

// The catalog authenticates creation order independently of mutable filesystem
// timestamps. Missing or unverifiable catalog evidence never authorizes deletion.
func loadManagedCatalog(v *Vault) (managedCatalogState, error) {
	path := filepath.Join(v.directory, managedCatalogFilename)
	data, err := readCipherFile(path, maxManagedCatalogBytes)
	if os.IsNotExist(err) {
		return managedCatalogState{managedCatalog: managedCatalog{Version: 1, NextSequence: 1, Entries: []managedCatalogEntry{}}}, nil
	}
	if err != nil {
		return managedCatalogState{}, err
	}
	key, err := v.deriveKeyLocked("managed-backup-catalog/v1")
	if err != nil {
		return managedCatalogState{}, err
	}
	aead, err := rootAEAD(key)
	clear(key)
	if err != nil || !bytes.HasPrefix(data, managedCatalogHeader) || len(data) < len(managedCatalogHeader)+aead.NonceSize()+aead.Overhead() {
		return managedCatalogState{}, ErrCorrupt
	}
	plain, err := aead.Open(nil, data[len(managedCatalogHeader):len(managedCatalogHeader)+aead.NonceSize()], data[len(managedCatalogHeader)+aead.NonceSize():], managedCatalogHeader)
	if err != nil {
		return managedCatalogState{}, ErrCorrupt
	}
	defer clear(plain)
	var catalog managedCatalog
	if decodeCanonical(plain, &catalog) != nil || !validManagedCatalog(catalog) {
		return managedCatalogState{}, ErrCorrupt
	}
	return managedCatalogState{managedCatalog: catalog, exists: true, observed: sha256.Sum256(data)}, nil
}

func validManagedCatalog(catalog managedCatalog) bool {
	if catalog.Version != 1 || catalog.NextSequence == 0 || catalog.Entries == nil || len(catalog.Entries) > maxManagedCatalogEntries {
		return false
	}
	names := make(map[string]bool, len(catalog.Entries))
	var previous uint64
	for _, entry := range catalog.Entries {
		digest, err := hex.DecodeString(entry.Digest)
		if !migrationBackupName.MatchString(entry.Name) || names[entry.Name] || err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != entry.Digest || entry.Sequence <= previous || entry.Sequence >= catalog.NextSequence {
			return false
		}
		names[entry.Name] = true
		previous = entry.Sequence
	}
	return true
}

func saveManagedCatalog(v *Vault, state *managedCatalogState) error {
	if state == nil || !validManagedCatalog(state.managedCatalog) {
		return ErrState
	}
	key, err := v.deriveKeyLocked("managed-backup-catalog/v1")
	if err != nil {
		return err
	}
	aead, err := rootAEAD(key)
	clear(key)
	if err != nil {
		return err
	}
	plain, err := json.Marshal(state.managedCatalog)
	if err != nil {
		return err
	}
	defer clear(plain)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	wire := append(bytes.Clone(managedCatalogHeader), nonce...)
	wire = aead.Seal(wire, nonce, plain, managedCatalogHeader)
	if len(wire) > maxManagedCatalogBytes {
		return ErrState
	}
	path := filepath.Join(v.directory, managedCatalogFilename)
	if state.exists {
		previous, err := readCipherFile(path, maxManagedCatalogBytes)
		if err != nil || sha256.Sum256(previous) != state.observed {
			return ErrCorrupt
		}
		if err := atomicCipherReplace(path, wire); err != nil {
			return err
		}
	} else {
		// Publish without replacing a file that appeared after the missing-file
		// observation. An unverified existing catalog requires explicit repair.
		f, err := os.CreateTemp(v.directory, ".protection-*.tmp")
		if err != nil {
			return err
		}
		temp := f.Name()
		defer os.Remove(temp)
		if f.Chmod(0600) != nil {
			f.Close()
			return ErrUnsafePath
		}
		if n, err := f.Write(wire); err != nil || n != len(wire) {
			f.Close()
			return ErrState
		}
		syncErr, closeErr := f.Sync(), f.Close()
		if syncErr != nil || closeErr != nil {
			return ErrState
		}
		if os.Link(temp, path) != nil || syncDirectory(v.directory) != nil {
			return ErrState
		}
	}
	state.exists = true
	state.observed = sha256.Sum256(wire)
	return nil
}

func managedDigest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func readableCurrentMasterRoot(v *Vault, data []byte) bool {
	if !bytes.HasPrefix(data, rootHeader) {
		return false
	}
	scratch := scratchVault(v)
	defer clearScratch(scratch)
	scratch.passSalt, scratch.passKey = bytes.Clone(v.passSalt), bytes.Clone(v.passKey)
	plain, err := scratch.loadRootLocked(data[len(rootHeader):])
	clear(plain)
	return err == nil && bytes.Equal(scratch.master, v.master)
}

// Call only immediately after publishing a new complete backup. A crash before
// this optional registration leaves an unlisted copy which retention preserves.
func recordManagedBackup(v *Vault, name string, data []byte) error {
	if !migrationBackupName.MatchString(name) || !readableCurrentMasterRoot(v, data) {
		return ErrCorrupt
	}
	actual, err := readCipherFile(filepath.Join(v.directory, name), maxRootFileBytes)
	if err != nil || !bytes.Equal(actual, data) {
		return ErrCorrupt
	}
	state, err := loadManagedCatalog(v)
	if err != nil {
		return err
	}
	digest := managedDigest(data)
	for _, entry := range state.Entries {
		if entry.Name == name {
			if entry.Digest == digest {
				return nil
			}
			return ErrCorrupt
		}
	}
	if len(state.Entries) >= maxManagedCatalogEntries || state.NextSequence == math.MaxUint64 {
		return ErrState
	}
	state.Entries = append(state.Entries, managedCatalogEntry{Name: name, Digest: digest, Sequence: state.NextSequence})
	state.NextSequence++
	return saveManagedCatalog(v, &state)
}

// Mandatory root protection remains independent of optional catalog maintenance.
// Only a matching authenticated pre-rewrap entry can retain its original order.
func replaceManagedRootTracked(v *Vault, path string, sealed []byte, operations managedCopyIO) error {
	var state managedCatalogState
	index := -1
	if filepath.Dir(path) == v.directory && migrationBackupName.MatchString(filepath.Base(path)) {
		if current, err := loadManagedCatalog(v); err == nil && current.exists {
			if previous, err := readCipherFile(path, maxManagedRootBytes); err == nil {
				for i, entry := range current.Entries {
					if entry.Name == filepath.Base(path) && entry.Digest == managedDigest(previous) {
						state, index = current, i
						break
					}
				}
			}
		}
	}
	if err := operations.replaceRoot(path, sealed); err != nil {
		return err
	}
	if index >= 0 {
		state.Entries[index].Digest = managedDigest(sealed)
		_ = saveManagedCatalog(v, &state)
	}
	return nil
}

func pruneCatalogedManagedCopies(a *ApplicationState, operations managedCopyIO) error {
	state, err := loadManagedCatalog(a.vault)
	if err != nil || !state.exists {
		return err
	}
	type candidate struct {
		path  string
		info  os.FileInfo
		entry managedCatalogEntry
	}
	var eligible []candidate
	pinned := a.state.MigrationBackup
	for _, entry := range state.Entries {
		if entry.Name == pinned {
			continue
		}
		path := filepath.Join(a.vault.directory, entry.Name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || !recoveryOwned(info) || regular(path) != nil {
			continue
		}
		data, err := readCipherFile(path, maxRootFileBytes)
		if err != nil || managedDigest(data) != entry.Digest || !readableCurrentMasterRoot(a.vault, data) {
			continue
		}
		eligible = append(eligible, candidate{path: path, info: info, entry: entry})
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].entry.Sequence > eligible[j].entry.Sequence })
	deleted := map[string]bool{}
	for index := maxRetainedMigrationBackups; index < len(eligible); index++ {
		item := eligible[index]
		info, err := os.Lstat(item.path)
		data, readErr := readCipherFile(item.path, maxRootFileBytes)
		if err != nil || !os.SameFile(info, item.info) || readErr != nil || managedDigest(data) != item.entry.Digest {
			return ErrUnsafePath
		}
		if operations.removeRoot(item.path) != nil || syncDirectory(a.vault.directory) != nil {
			return ErrState
		}
		deleted[item.entry.Name] = true
	}
	if len(deleted) > 0 {
		kept := state.Entries[:0]
		for _, entry := range state.Entries {
			if !deleted[entry.Name] {
				kept = append(kept, entry)
			}
		}
		state.Entries = kept
		return saveManagedCatalog(a.vault, &state)
	}
	return nil
}
