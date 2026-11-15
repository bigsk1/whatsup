package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
	"whatsup/internal/keymaterial"
)

const recoveryJournalFilename = "recovery-install.vault"

var recoveryJournalHeader = []byte("WUPRESTORE\x01")

type recoveryJournal struct {
	Version            int      `json:"version"`
	StageID            string   `json:"stageID"`
	RollbackID         string   `json:"rollbackID"`
	RootHash           string   `json:"rootSHA256"`
	DatabaseHash       string   `json:"databaseSHA256"`
	ObjectsHash        string   `json:"objectsSHA256"`
	OldNames           []string `json:"oldNames"`
	RequiresPassphrase bool     `json:"requiresPassphrase"`
	ResetCredential    bool     `json:"resetCredential"`
	ProtectedOld       bool     `json:"protectedOld"`
	Installed          bool     `json:"installed"`
	Contacts           int      `json:"contacts"`
	Messages           uint64   `json:"messages"`
	Attachments        uint64   `json:"attachments"`
}

func recoveryJournalKey(key []byte) ([]byte, error) {
	return keymaterial.Derive(key, "recovery-install-journal/v1")
}
func writeRecoveryJournal(directory string, key []byte, j recoveryJournal) error {
	plain, err := json.Marshal(j)
	if err != nil || len(plain) > 64<<10 {
		return ErrRecovery
	}
	defer clear(plain)
	derived, err := recoveryJournalKey(key)
	if err != nil {
		return err
	}
	defer clear(derived)
	aead, err := rootAEAD(derived)
	if err != nil {
		return err
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	data := append(append([]byte(nil), recoveryJournalHeader...), nonce...)
	data = aead.Seal(data, nonce, plain, recoveryJournalHeader)
	return atomicCipherReplace(filepath.Join(directory, recoveryJournalFilename), data)
}
func readRecoveryJournal(directory string, key []byte) (recoveryJournal, error) {
	var j recoveryJournal
	data, err := readCipherFile(filepath.Join(directory, recoveryJournalFilename), 64<<10)
	if err != nil {
		return j, err
	}
	if len(data) < len(recoveryJournalHeader)+40 || !bytes.HasPrefix(data, recoveryJournalHeader) {
		return j, ErrRecoveryPending
	}
	derived, err := recoveryJournalKey(key)
	if err != nil {
		return j, err
	}
	defer clear(derived)
	aead, _ := rootAEAD(derived)
	offset := len(recoveryJournalHeader)
	plain, err := aead.Open(nil, data[offset:offset+24], data[offset+24:], recoveryJournalHeader)
	if err != nil {
		return j, ErrRecoveryPending
	}
	defer clear(plain)
	if decodeCanonical(plain, &j) != nil || j.Version != 1 || !pendingIDValid(j.StageID) || !pendingIDValid(j.RollbackID) || len(j.OldNames) > 256 || j.Contacts < 0 || j.Contacts > MaxContacts {
		return j, ErrRecoveryPending
	}
	for _, hash := range []string{j.RootHash, j.DatabaseHash, j.ObjectsHash} {
		raw, err := hex.DecodeString(hash)
		if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != hash {
			return j, ErrRecoveryPending
		}
	}
	seen := map[string]bool{}
	for _, name := range j.OldNames {
		if !allowedRecoveryName(name) || seen[name] {
			return j, ErrRecoveryPending
		}
		seen[name] = true
	}
	return j, nil
}
func allowedRecoveryName(name string) bool {
	if name == managedCatalogFilename {
		return true
	}
	switch name {
	case "state.vault", "messages.sqlite", "messages.sqlite-wal", "messages.sqlite-shm", "messages.sqlite-journal", "attachments", "app-preferences.vault":
		return true
	}
	return migrationBackupName.MatchString(name)
}

func objectDirectoryDigest(ctx context.Context, directory string) (string, error) {
	if privateRecoveryDirectory(directory) != nil {
		return "", ErrUnsafePath
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name()) })
	hash := sha256.New()
	var previous string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".bin") || !pendingIDValid(strings.TrimSuffix(name, ".bin")) || entry.IsDir() || previous != "" && strings.EqualFold(previous, name) {
			return "", ErrRecovery
		}
		previous = name
		file, err := hashRecoveryFile(ctx, filepath.Join(directory, name))
		if err != nil {
			return "", err
		}
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write(file.SHA256[:])
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func recoveryComponentHash(ctx context.Context, directory, name string) (string, error) {
	if name == "attachments" {
		return objectDirectoryDigest(ctx, filepath.Join(directory, name))
	}
	entry, err := hashRecoveryFile(ctx, filepath.Join(directory, name))
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(entry.SHA256[:]), nil
}
func expectedRecoveryHash(j recoveryJournal, name string) string {
	switch name {
	case "state.vault":
		return j.RootHash
	case "messages.sqlite":
		return j.DatabaseHash
	case "attachments":
		return j.ObjectsHash
	}
	return ""
}

func protectLiveRootForRecovery(v *Vault) error {
	path := filepath.Join(v.directory, "state.vault")
	data, err := readCipherFile(path, maxManagedRootBytes)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if bytes.HasPrefix(data, preservedRootHeader) {
		plain, err := openPreservedRoot(v, path, data)
		clear(plain)
		if err == nil {
			return nil
		}
		return preserveManagedRoot(v, path, data, managedCopyIO{}, managedCopyPolicy{preserveOpaque: true}, err)
	}
	if bytes.HasPrefix(data, rootHeader) {
		root, err := decodeRoot(data[len(rootHeader):])
		if err != nil {
			return preserveManagedRoot(v, path, data, managedCopyIO{}, managedCopyPolicy{preserveOpaque: true}, err)
		}
		if root.Mode == "os-plus-passphrase" {
			if rootUsesCurrentProtection(v, root) {
				plain, err := managedPayload(root, v.master)
				clear(plain)
				if err != nil {
					return preserveManagedRoot(v, path, data, managedCopyIO{}, managedCopyPolicy{preserveOpaque: true}, err)
				}
				return nil
			}
			sealed, same, err := rewrapSameMasterRoot(v, data)
			if err != nil {
				return err
			}
			if same {
				return atomicCipherReplace(path, sealed)
			}
			if knownRootInaccessibleWithOSKey(v, data) {
				return nil
			}
			return preserveManagedRoot(v, path, data, managedCopyIO{}, managedCopyPolicy{preserveOpaque: true}, ErrCorrupt)
		}
	}
	scratch := scratchVault(v)
	defer clearScratch(scratch)
	plain, err := decodeRootWithSecret(scratch, data, nil)
	if err != nil {
		if knownRootInaccessibleWithOSKey(scratch, data) {
			return nil
		}
		return preserveManagedRoot(v, path, data, managedCopyIO{}, managedCopyPolicy{preserveOpaque: true}, err)
	}
	defer clear(plain)
	if len(scratch.master) != 32 {
		scratch.master, err = keymaterial.RandomKey()
		if err != nil {
			return err
		}
	}
	scratch.protectionMode = "os-plus-passphrase"
	scratch.passSalt = bytes.Clone(v.passSalt)
	scratch.passKey = bytes.Clone(v.passKey)
	sealed, err := scratch.sealRootLocked(plain)
	if err != nil {
		return err
	}
	return atomicCipherReplace(path, sealed)
}

// Reconciliation needs only the OS key for an ordinary prepared install. A
// protected replacement first asks for its chosen app secret before publishing
// a new root, so an old OS-only root cannot become a managed backup bypass.
func reconcileRecoveryDirectory(directory string, key, secret []byte) error {
	return reconcileRecoveryWithStep(directory, key, secret, nil)
}

func reconcileRecoveryWithStep(directory string, key, secret []byte, step func(string) error) error {
	return reconcileRecoveryContext(context.Background(), directory, key, secret, step)
}

func reconcileRecoveryContext(ctx context.Context, directory string, key, secret []byte, step func(string) error) error {
	if ctx == nil {
		return ErrRecovery
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	j, err := readRecoveryJournal(directory, key)
	if err != nil {
		return err
	}
	stage := filepath.Join(directory, ".recovery-stage-"+j.StageID)
	rollback := filepath.Join(directory, ".recovery-rollback-"+j.RollbackID)
	if !j.ProtectedOld {
		if j.RequiresPassphrase && !j.ResetCredential {
			if len(secret) == 0 {
				return ErrPassphraseRequired
			}
			data, err := readCipherFile(filepath.Join(stage, "state.vault"), maxRootFileBytes)
			if err != nil {
				return err
			}
			scratch := &Vault{&vaultState{directory: directory, key: bytes.Clone(key)}}
			plain, err := decodeRootWithSecret(scratch, data, secret)
			clear(plain)
			if err != nil {
				clearScratch(scratch)
				return err
			}
			if err := protectRecoveryCopiesLocked(scratch, filepath.Join(stage, "state.vault")); err != nil {
				clearScratch(scratch)
				return err
			}
			if err := protectLiveRootForRecovery(scratch); err != nil {
				clearScratch(scratch)
				return err
			}
			clearScratch(scratch)
		}
		j.ProtectedOld = true
		if err := writeRecoveryJournal(directory, key, j); err != nil {
			return err
		}
		if step != nil {
			if err := step("protected"); err != nil {
				return err
			}
		}
	}
	if _, err := os.Lstat(rollback); os.IsNotExist(err) {
		if os.Mkdir(rollback, 0700) != nil {
			return ErrRecovery
		}
	}
	if privateRecoveryDirectory(rollback) != nil {
		return ErrUnsafePath
	}
	// Old SQLite sidecars and the unreadable credential-dependent preferences
	// have no new counterpart. Preserve them before installing the new DB.
	for _, name := range j.OldNames {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if name == "state.vault" || name == "messages.sqlite" || name == "attachments" {
			continue
		}
		current, old := filepath.Join(directory, name), filepath.Join(rollback, name)
		if _, err := os.Lstat(old); err == nil {
			continue
		}
		if _, err := os.Lstat(current); os.IsNotExist(err) {
			continue
		}
		if regular(current) != nil {
			return ErrUnsafePath
		}
		if os.Rename(current, old) != nil {
			return ErrRecovery
		}
	}
	for _, name := range []string{"messages.sqlite", "attachments", "state.vault"} {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		want := expectedRecoveryHash(j, name)
		current, from, old := filepath.Join(directory, name), filepath.Join(stage, name), filepath.Join(rollback, name)
		if actual, err := recoveryComponentHash(ctx, directory, name); err == nil && actual == want {
			continue
		}
		if actual, err := recoveryComponentHash(ctx, stage, name); err != nil || actual != want {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrRecoveryPending
		}
		if _, err := os.Lstat(current); err == nil {
			if name == "attachments" {
				if privateRecoveryDirectory(current) != nil {
					return ErrUnsafePath
				}
			} else if regular(current) != nil {
				return ErrUnsafePath
			}
			if _, err := os.Lstat(old); !os.IsNotExist(err) {
				return ErrRecoveryPending
			}
			if os.Rename(current, old) != nil {
				return ErrRecovery
			}
		} else if !os.IsNotExist(err) {
			return ErrRecovery
		}
		if os.Rename(from, current) != nil || syncDirectory(directory) != nil || syncDirectory(stage) != nil || syncDirectory(rollback) != nil {
			return ErrRecovery
		}
		if step != nil {
			if err := step(name); err != nil {
				return err
			}
		}
	}
	j.Installed = true
	return writeRecoveryJournal(directory, key, j)
}

func (r *RecoveryImport) Commit(ctx context.Context, keys KeyStore, newAppSecret []byte, confirmReplace bool) (result error) {
	if r == nil || r.recoveryImportState == nil || ctx == nil || keys == nil {
		return ErrRecovery
	}
	if len(newAppSecret) > 0 && keymaterial.ValidateSecret(newAppSecret) != nil {
		return ErrPassphrase
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.committed || !r.verified.Valid() {
		return ErrRecovery
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if backendRecoveryDataExists(r.directory) && !confirmReplace {
		return ErrRecoveryReplace
	}
	lockPath := filepath.Join(r.directory, "state.lock")
	if regular(lockPath) != nil {
		return ErrUnsafePath
	}
	lock := flock.New(lockPath, flock.SetPermissions(0600))
	ok, err := lock.TryLock()
	if err != nil || !ok {
		lock.Close()
		return ErrBusy
	}
	defer lock.Close()
	encoded, err := keys.Get()
	reset := errors.Is(err, keyring.ErrNotFound)
	var key []byte
	if reset {
		if backendRecoveryDataExists(r.directory) && !confirmReplace {
			return ErrRecoveryReplace
		}
		key, err = keymaterial.RandomKey()
		if err != nil {
			return err
		}
		encoded = base64.RawStdEncoding.EncodeToString(key)
	} else if err != nil {
		return ErrCredential
	} else {
		key, err = decodeCredential(encoded)
		if err != nil {
			return err
		}
	}
	defer clear(key)
	if err := r.validateStage(ctx); err != nil {
		return err
	}
	payload, err := json.Marshal(r.portable.State)
	if err != nil {
		return ErrRecovery
	}
	defer clear(payload)
	candidate := &Vault{&vaultState{directory: r.stage, key: bytes.Clone(key), master: bytes.Clone(r.master), masterPersisted: true, protectionMode: "os-only"}}
	defer clearScratch(candidate)
	if len(newAppSecret) > 0 {
		candidate.protectionMode = "os-plus-passphrase"
		candidate.passSalt, err = keymaterial.PassphraseSalt()
		if err != nil {
			return err
		}
		candidate.passKey, err = derivePassphrase(newAppSecret, candidate.passSalt)
		if err != nil {
			return err
		}
	}
	if err := candidate.saveLocked(payload); err != nil {
		return err
	}
	if r.testStep != nil {
		if err := r.testStep("candidate"); err != nil {
			return err
		}
	}
	rollbackID, err := randomStoreID()
	if err != nil {
		return err
	}
	j := recoveryJournal{Version: 1, StageID: r.stageID, RollbackID: rollbackID, RequiresPassphrase: len(newAppSecret) > 0, ResetCredential: reset, Contacts: r.summary.Contacts, Messages: r.summary.Messages, Attachments: r.summary.Attachments}
	j.RootHash, err = recoveryComponentHash(ctx, r.stage, "state.vault")
	if err != nil {
		return err
	}
	j.DatabaseHash, err = recoveryComponentHash(ctx, r.stage, "messages.sqlite")
	if err != nil {
		return err
	}
	j.ObjectsHash, err = recoveryComponentHash(ctx, r.stage, "attachments")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(r.directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if allowedRecoveryName(name) && (name != "app-preferences.vault" || reset) {
			j.OldNames = append(j.OldNames, name)
		}
	}
	// A lost credential makes any older journal unreadable. Preserve its exact
	// ciphertext only during this fully authenticated, explicitly confirmed path.
	if _, err := os.Lstat(filepath.Join(r.directory, recoveryJournalFilename)); err == nil {
		if !confirmReplace {
			return ErrRecoveryReplace
		}
		if !reset {
			return ErrRecoveryPending
		}
		rollback := filepath.Join(r.directory, ".recovery-rollback-"+rollbackID)
		if os.Mkdir(rollback, 0700) != nil {
			return ErrRecovery
		}
		if regular(filepath.Join(r.directory, recoveryJournalFilename)) != nil || os.Rename(filepath.Join(r.directory, recoveryJournalFilename), filepath.Join(rollback, "interrupted-recovery.vault")) != nil {
			return ErrRecovery
		}
	}
	if err := writeRecoveryJournal(r.directory, key, j); err != nil {
		return err
	}
	r.committed = true
	if r.testStep != nil {
		if err := r.testStep("journal"); err != nil {
			return err
		}
	}
	if reset {
		if keys.Set(encoded) != nil {
			return ErrCredential
		}
		stored, err := keys.Get()
		if err != nil || stored != encoded {
			return ErrCredential
		}
	}
	if r.testStep != nil {
		if err := r.testStep("credential"); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := reconcileRecoveryContext(ctx, r.directory, key, newAppSecret, r.testStep); err != nil {
		return err
	}
	// The process lock remains held while a fresh vault/application verifies the
	// installed root and stores. No controller is started by this verification.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	v := &Vault{&vaultState{directory: r.directory, key: bytes.Clone(key), recoveryContext: ctx}}
	defer v.Close()
	data, err := readCipherFile(filepath.Join(r.directory, "state.vault"), maxRootFileBytes)
	if err != nil {
		return err
	}
	plain, err := decodeRootWithSecret(v, data, newAppSecret)
	clear(plain)
	if err != nil {
		return err
	}
	if _, err := OpenApplication(v); err != nil {
		return err
	}
	return nil
}

func (a *ApplicationState) finishRecoveryLocked() error {
	path := filepath.Join(a.vault.directory, recoveryJournalFilename)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	}
	j, err := readRecoveryJournal(a.vault.directory, a.vault.key)
	if err != nil || !j.Installed {
		return ErrRecoveryPending
	}
	ctx := a.vault.recoveryContext
	if ctx == nil {
		ctx = context.Background()
	}
	_, summary, err := a.snapshotObjects(ctx)
	if err != nil || summary.Contacts != j.Contacts || summary.Messages != j.Messages || summary.Attachments != j.Attachments {
		return ErrRecovery
	}
	if err := protectRecoveryCopiesLocked(a.vault, filepath.Join(a.vault.directory, "state.vault")); err != nil {
		return err
	}
	if os.Remove(path) != nil || syncDirectory(a.vault.directory) != nil {
		return ErrRecovery
	}
	return removeRecoveryStage(a.vault.directory, j.StageID)
}
