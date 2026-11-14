package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"whatsup/internal/keymaterial"
)

type ProtectionStatus struct {
	Exists             bool `json:"exists"`
	RequiresPassphrase bool `json:"requiresPassphrase"`
}

// ManagedCopyError identifies a backup which cannot safely be reconciled. Do not
// silently ignore malformed roots: an intact OS slot in a damaged payload can
// still expose the master key. Paths are relative, recognized app-owned names.
type ManagedCopyError struct {
	RelativePath string
	Cause        error
}

func (e *ManagedCopyError) Error() string {
	return "a managed backup requires attention: " + e.RelativePath
}
func (e *ManagedCopyError) Unwrap() error { return e.Cause }

func managedCopyError(v *Vault, path string, err error) error {
	rel, relativeErr := filepath.Rel(v.directory, path)
	if relativeErr != nil {
		return err
	}
	return &ManagedCopyError{RelativePath: rel, Cause: err}
}

// Per-call filesystem fault boundary, matching rootBackupIO. Production always
// uses the atomic writer and regular unlink; tests can interrupt one transition.
type managedCopyIO struct {
	replace func(string, []byte) error
	remove  func(string) error
}

func (operations managedCopyIO) replaceRoot(path string, data []byte) error {
	if operations.replace != nil {
		return operations.replace(path, data)
	}
	return replaceManagedRoot(path, data)
}
func (operations managedCopyIO) removeRoot(path string) error {
	if operations.remove != nil {
		return operations.remove(path)
	}
	return os.Remove(path)
}

func readCipherFile(path string, maximum int64) ([]byte, error) {
	if err := regular(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := os.Lstat(path)
	if err != nil {
		return nil, ErrUnsafePath
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, ErrUnsafePath
	}
	data, err := io.ReadAll(io.LimitReader(f, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, ErrCorrupt
	}
	return data, nil
}

// Inspection is only a bounded startup hint. It creates no files/credentials,
// and authenticated opening remains the authority for all data access.
func InspectProtection(directory string) (ProtectionStatus, error) {
	if !filepath.IsAbs(directory) {
		return ProtectionStatus{}, ErrUnsafePath
	}
	data, err := readCipherFile(filepath.Join(directory, "state.vault"), maxRootFileBytes)
	if os.IsNotExist(err) {
		return ProtectionStatus{}, nil
	}
	if err != nil {
		return ProtectionStatus{}, err
	}
	if bytes.HasPrefix(data, header) {
		return ProtectionStatus{Exists: true}, nil
	}
	if !bytes.HasPrefix(data, rootHeader) {
		return ProtectionStatus{}, ErrCorrupt
	}
	root, err := decodeRoot(data[len(rootHeader):])
	if err != nil {
		return ProtectionStatus{}, err
	}
	return ProtectionStatus{Exists: true, RequiresPassphrase: root.Mode == "os-plus-passphrase"}, nil
}

// Pending replacement policy is authenticated with an existing OS credential.
// This inspection never creates/replaces a credential or advances installation.
func InspectProtectionWithKeys(directory string, keys KeyStore) (ProtectionStatus, error) {
	if keys == nil || !filepath.IsAbs(directory) {
		return ProtectionStatus{}, ErrCredential
	}
	if _, err := os.Lstat(filepath.Join(directory, recoveryJournalFilename)); os.IsNotExist(err) {
		return InspectProtection(directory)
	}
	encoded, err := keys.Get()
	if err != nil {
		return ProtectionStatus{}, ErrCredential
	}
	key, err := decodeCredential(encoded)
	if err != nil {
		return ProtectionStatus{}, err
	}
	defer clear(key)
	journal, err := readRecoveryJournal(directory, key)
	if err != nil {
		return ProtectionStatus{}, err
	}
	return ProtectionStatus{Exists: true, RequiresPassphrase: journal.RequiresPassphrase}, nil
}

func derivePassphrase(secret, salt []byte) ([]byte, error) {
	if keymaterial.ValidateSecret(secret) != nil {
		return nil, ErrPassphrase
	}
	raw, err := keymaterial.PassphraseKey(secret, salt)
	if err != nil {
		return nil, ErrPassphrase
	}
	defer clear(raw)
	return keymaterial.Derive(raw, "passphrase-master-wrap/v1")
}

func OpenWithPassphrase(directory string, keys KeyStore, secret []byte) (*Vault, error) {
	if len(secret) > 0 && keymaterial.ValidateSecret(secret) != nil {
		return nil, ErrPassphrase
	}
	v, err := openWithSecret(directory, keys, secret)
	if err != nil {
		return nil, err
	}
	data, err := readCipherFile(filepath.Join(directory, "state.vault"), maxRootFileBytes)
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		_ = v.Close()
		return nil, err
	}
	if bytes.HasPrefix(data, rootHeader) {
		root, decodeErr := decodeRoot(data[len(rootHeader):])
		if decodeErr != nil {
			_ = v.Close()
			return nil, decodeErr
		}
		if root.Mode == "os-plus-passphrase" {
			if len(secret) == 0 {
				_ = v.Close()
				return nil, ErrPassphraseRequired
			}
			v.passSalt = bytes.Clone(root.Slots.Passphrase.Salt)
			v.passKey, err = derivePassphrase(secret, v.passSalt)
			if err != nil {
				_ = v.Close()
				return nil, err
			}
		}
	}
	return v, nil
}

func (a *ApplicationState) Protection() (ProtectionStatus, error) {
	if a == nil || a.vault == nil {
		return ProtectionStatus{}, ErrLocked
	}
	a.vault.mu.Lock()
	defer a.vault.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return ProtectionStatus{}, err
	}
	return ProtectionStatus{Exists: true, RequiresPassphrase: a.vault.protectionMode == "os-plus-passphrase"}, nil
}

func scratchVault(v *Vault) *Vault {
	return &Vault{&vaultState{directory: v.directory, key: bytes.Clone(v.key)}}
}
func clearScratch(v *Vault) {
	clear(v.key)
	v.key = nil
	v.clearMasterLocked()
	clear(v.passKey)
	v.passKey = nil
	clear(v.passSalt)
	v.passSalt = nil
}

func decodeRootWithSecret(v *Vault, data, secret []byte) ([]byte, error) {
	if bytes.HasPrefix(data, rootHeader) {
		root, err := decodeRoot(data[len(rootHeader):])
		if err != nil {
			return nil, err
		}
		if root.Mode == "os-plus-passphrase" {
			if len(secret) == 0 {
				return nil, ErrPassphraseRequired
			}
			clear(v.passSalt)
			v.passSalt = bytes.Clone(root.Slots.Passphrase.Salt)
			clear(v.passKey)
			v.passKey, err = derivePassphrase(secret, v.passSalt)
			if err != nil {
				return nil, err
			}
		}
		return v.loadRootLocked(data[len(rootHeader):])
	}
	if !bytes.HasPrefix(data, header) {
		return nil, ErrCorrupt
	}
	aead, err := v.aead()
	if err != nil || len(data) < len(header)+aead.NonceSize()+aead.Overhead() {
		return nil, ErrCorrupt
	}
	plain, err := aead.Open(nil, data[len(header):len(header)+aead.NonceSize()], data[len(header)+aead.NonceSize():], header)
	if err != nil {
		return nil, ErrCorrupt
	}
	v.legacy = true
	return plain, nil
}

func atomicCipherReplace(path string, data []byte) error {
	if err := regular(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".protection-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if f.Chmod(0600) != nil {
		_ = f.Close()
		return ErrUnsafePath
	}
	if n, err := f.Write(data); err != nil || n != len(data) {
		_ = f.Close()
		return ErrState
	}
	if f.Sync() != nil {
		_ = f.Close()
		return ErrState
	}
	if f.Close() != nil {
		return ErrState
	}
	if os.Rename(temp, path) != nil || syncDirectory(dir) != nil {
		return ErrState
	}
	return nil
}

var recoveryRollbackName = regexp.MustCompile(`^\.recovery-rollback-[A-Za-z0-9_-]{22}$`)
var recoveryStageName = regexp.MustCompile(`^\.recovery-stage-[A-Za-z0-9_-]{22}$`)
var ownedRootTemporaryName = regexp.MustCompile(`^\.(state|protection)-[0-9]+\.tmp$`)

func managedMasterAAD(root rootEnvelope) []byte {
	wire := append(bytes.Clone(root.Payload.Nonce), root.Payload.Ciphertext...)
	hash := sha256.Sum256(wire)
	return append([]byte("WhatsUp managed copy master v1\x00"), hash[:]...)
}
func managedPayload(root rootEnvelope, master []byte) ([]byte, error) {
	key, err := keymaterial.Derive(master, "vault-envelope/v2")
	if err != nil {
		return nil, err
	}
	aead, err := rootAEAD(key)
	clear(key)
	if err != nil {
		return nil, err
	}
	aad, err := rootAAD(rootMetadata{Version: root.Version, Mode: root.Mode, Slots: root.Slots})
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, root.Payload.Nonce, root.Payload.Ciphertext, aad)
}
func sealManagedRoot(owner *Vault, master, plain []byte) ([]byte, error) {
	scratch := scratchVault(owner)
	defer clearScratch(scratch)
	scratch.master = bytes.Clone(master)
	scratch.masterPersisted = true
	scratch.protectionMode = owner.protectionMode
	scratch.passSalt = bytes.Clone(owner.passSalt)
	scratch.passKey = bytes.Clone(owner.passKey)
	sealed, err := scratch.sealRootLocked(plain)
	if err != nil || bytes.Equal(master, owner.master) {
		return sealed, err
	}
	root, err := decodeRoot(sealed[len(rootHeader):])
	if err != nil {
		return nil, err
	}
	key, err := keymaterial.Derive(owner.master, "managed-copy-master/v1")
	if err != nil {
		return nil, err
	}
	aead, _ := rootAEAD(key)
	clear(key)
	link, err := sealRootValue(aead, master, managedMasterAAD(root))
	if err != nil {
		return nil, err
	}
	root.ManagedMaster = &link
	encoded, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	return append(bytes.Clone(rootHeader), encoded...), nil
}

// A historical passphrase is not needed to identify roots under the already
// unlocked master. The payload AEAD binds the complete old slot metadata.
func rewrapSameMasterRoot(v *Vault, data []byte) ([]byte, bool, error) {
	if !bytes.HasPrefix(data, rootHeader) || len(v.master) != 32 {
		return nil, false, nil
	}
	root, err := decodeRoot(data[len(rootHeader):])
	if err != nil {
		return nil, false, err
	}
	master := bytes.Clone(v.master)
	defer func() { clear(master) }()
	plain, err := managedPayload(root, master)
	if err != nil && root.ManagedMaster != nil {
		key, deriveErr := keymaterial.Derive(v.master, "managed-copy-master/v1")
		if deriveErr != nil {
			return nil, false, deriveErr
		}
		aead, _ := rootAEAD(key)
		clear(key)
		linked, linkErr := aead.Open(nil, root.ManagedMaster.Nonce, root.ManagedMaster.Ciphertext, managedMasterAAD(root))
		if linkErr == nil && len(linked) == 32 {
			clear(master)
			master = linked
			plain, err = managedPayload(root, master)
		} else {
			clear(linked)
		}
	}
	if err != nil {
		return nil, false, nil
	}
	defer clear(plain)
	sealed, err := sealManagedRoot(v, master, plain)
	return sealed, true, err
}

func rootUsesCurrentProtection(v *Vault, root rootEnvelope) bool {
	if root.Mode != "os-plus-passphrase" || root.Slots.Passphrase == nil || len(v.passKey) != 32 || !bytes.Equal(root.Slots.Passphrase.Salt, v.passSalt) {
		return false
	}
	key, err := keymaterial.Derive(v.key, "os-master-wrap/v2")
	if err != nil {
		return false
	}
	outer, _ := rootAEAD(key)
	clear(key)
	inner, err := outer.Open(nil, root.Slots.OS.Nonce, root.Slots.OS.Ciphertext, passphraseAAD(root.Slots.Passphrase, "os-layer"))
	if err != nil {
		return false
	}
	defer clear(inner)
	pass, _ := rootAEAD(v.passKey)
	master, err := pass.Open(nil, root.Slots.Passphrase.Nonce, inner, passphraseAAD(root.Slots.Passphrase, "master"))
	defer clear(master)
	return err == nil && bytes.Equal(master, v.master)
}

// Recovery can wrap a rollback's different master using the newly chosen slots
// before installing the new live root. Authenticate the entire historical root
// with the already-derived current factors, then link its master for subsequent
// protection changes. Successful slot unwrap alone is insufficient.
func rewrapReadableManagedRoot(v *Vault, data []byte) ([]byte, bool, error) {
	scratch := scratchVault(v)
	defer clearScratch(scratch)
	scratch.passSalt, scratch.passKey = bytes.Clone(v.passSalt), bytes.Clone(v.passKey)
	plain, err := scratch.loadRootLocked(data[len(rootHeader):])
	defer clear(plain)
	if err != nil {
		return nil, false, nil
	}
	sealed, err := sealManagedRoot(v, scratch.master, plain)
	return sealed, true, err
}

// Rewrapping changes protection, not snapshot age. Retention must continue to
// order historical snapshots by their original time after the atomic replace.
func replaceManagedRoot(path string, sealed []byte) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err := atomicCipherReplace(path, sealed); err != nil {
		return err
	}
	return os.Chtimes(path, info.ModTime(), info.ModTime())
}

// Recovery moves roots and migration backups one level below the profile. Keep
// that same fixed boundary for protection, crash-temp cleanup and retention;
// unrelated files, nested trees, databases and attachment objects are not roots.
func managedRootPaths(v *Vault) ([]string, error) {
	entries, err := os.ReadDir(v.directory)
	if err != nil {
		return nil, err
	}
	containers := []string{v.directory}
	for _, entry := range entries {
		if recoveryRollbackName.MatchString(entry.Name()) || recoveryStageName.MatchString(entry.Name()) {
			path := filepath.Join(v.directory, entry.Name())
			if privateRecoveryDirectory(path) != nil {
				return nil, managedCopyError(v, path, ErrUnsafePath)
			}
			containers = append(containers, path)
		}
	}
	var paths []string
	for _, directory := range containers {
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, err
		}
		removed := false
		for _, entry := range entries {
			temporary := ownedRootTemporaryName.MatchString(entry.Name())
			if !temporary && !migrationBackupName.MatchString(entry.Name()) && !(directory != v.directory && entry.Name() == "state.vault") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || !recoveryOwned(info) || info.Size() < 0 || info.Size() > maxRootFileBytes || regular(path) != nil {
				return nil, managedCopyError(v, path, ErrUnsafePath)
			}
			if temporary {
				if os.Remove(path) != nil {
					return nil, ErrState
				}
				removed = true
			} else {
				paths = append(paths, path)
			}
		}
		if removed && syncDirectory(directory) != nil {
			return nil, ErrState
		}
	}
	return paths, nil
}

// Protected historical copies remain protected; every OS-only managed copy is
// rewritten before publishing protection, including backups moved by recovery.
func protectManagedCopiesLocked(v *Vault, currentSecrets ...[]byte) error {
	return protectManagedCopiesWithIOLocked(v, managedCopyIO{}, currentSecrets...)
}

func protectManagedCopiesWithIOLocked(v *Vault, operations managedCopyIO, currentSecrets ...[]byte) error {
	paths, err := managedRootPaths(v)
	if err != nil {
		return err
	}
	for _, path := range paths {
		data, err := readCipherFile(path, maxRootFileBytes)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return managedCopyError(v, path, err)
		}
		if bytes.HasPrefix(data, rootHeader) {
			root, err := decodeRoot(data[len(rootHeader):])
			if err != nil {
				return managedCopyError(v, path, err)
			}
			if root.Mode == "os-plus-passphrase" {
				if rootUsesCurrentProtection(v, root) {
					plain, err := managedPayload(root, v.master)
					clear(plain)
					if err != nil {
						return managedCopyError(v, path, ErrCorrupt)
					}
					continue
				}
				sealed, same, err := rewrapSameMasterRoot(v, data)
				if err != nil {
					return err
				}
				if !same {
					sealed, same, err = rewrapReadableManagedRoot(v, data)
					if err != nil {
						return err
					}
				}
				if same {
					if err := operations.replaceRoot(path, sealed); err != nil {
						return managedCopyError(v, path, err)
					}
				}
				if !same && len(currentSecrets) > 0 && len(currentSecrets[0]) > 0 {
					old := scratchVault(v)
					plain, err := decodeRootWithSecret(old, data, currentSecrets[0])
					if err == nil {
						sealed, err := sealManagedRoot(v, old.master, plain)
						clear(plain)
						clearScratch(old)
						if err != nil {
							return err
						}
						if err := operations.replaceRoot(path, sealed); err != nil {
							return managedCopyError(v, path, err)
						}
						same = true
					} else {
						clear(plain)
						clearScratch(old)
					}
				}
				// A damaged payload can retain usable old master-wrapping slots.
				// Only a different OS credential proves this copy inaccessible;
				// an unknown historical passphrase is not evidence of a different
				// master. Keep the bytes and require explicit repair instead.
				if !same && !knownRootInaccessibleWithOSKey(v, data) {
					return managedCopyError(v, path, ErrCorrupt)
				}
				continue
			}
			if v.protectionMode != "os-plus-passphrase" {
				continue // Already in the desired mode; preserve exact migration bytes.
			}
		}
		if v.protectionMode != "os-plus-passphrase" {
			continue // Legacy OS-only snapshots also retain their exact ciphertext.
		}
		scratch := scratchVault(v)
		plain, err := decodeRootWithSecret(scratch, data, nil)
		if err != nil {
			if knownRootInaccessibleWithOSKey(scratch, data) {
				clearScratch(scratch)
				continue
			}
			clearScratch(scratch)
			return managedCopyError(v, path, err)
		}
		if len(scratch.master) != 32 {
			scratch.master, err = keymaterial.RandomKey()
			if err != nil {
				clear(plain)
				clearScratch(scratch)
				return err
			}
		}
		sealed, err := sealManagedRoot(v, scratch.master, plain)
		clear(plain)
		clearScratch(scratch)
		if err != nil {
			return err
		}
		if err := operations.replaceRoot(path, sealed); err != nil {
			return managedCopyError(v, path, err)
		}
	}
	return nil
}

const maxRetainedMigrationBackups = 3

// Required protection is separate from best-effort retention. A cleanup failure
// must not invalidate healthy storage or misreport a committed mode change.
func (a *ApplicationState) finishManagedCopiesLocked() error {
	if err := protectManagedCopiesLocked(a.vault); err != nil && a.vault.protectionMode == "os-plus-passphrase" {
		return err
	}
	_ = a.pruneManagedCopiesLocked(managedCopyIO{})
	return nil
}

// Keep the newest three authenticated top-level snapshots under the current
// master, plus the live migration reference. Unreadable/different-key material
// does not displace a usable snapshot. Recovery trees are retained intact.
func (a *ApplicationState) pruneManagedCopiesLocked(operations managedCopyIO) error {
	if _, err := os.Lstat(filepath.Join(a.vault.directory, recoveryJournalFilename)); !os.IsNotExist(err) {
		return err
	}
	paths, err := managedRootPaths(a.vault)
	if err != nil {
		return err
	}
	type backup struct {
		path string
		info os.FileInfo
	}
	var backups []backup
	pinned := filepath.Join(a.vault.directory, a.state.MigrationBackup)
	for _, path := range paths {
		if filepath.Dir(path) != a.vault.directory || !migrationBackupName.MatchString(filepath.Base(path)) || path == pinned {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || !recoveryOwned(info) || regular(path) != nil {
			return ErrUnsafePath
		}
		data, err := readCipherFile(path, maxRootFileBytes)
		if err != nil {
			continue
		}
		scratch := scratchVault(a.vault)
		scratch.passSalt, scratch.passKey = bytes.Clone(a.vault.passSalt), bytes.Clone(a.vault.passKey)
		var plain []byte
		if bytes.HasPrefix(data, rootHeader) {
			plain, err = scratch.loadRootLocked(data[len(rootHeader):])
		} else {
			err = ErrCorrupt
		}
		readable := err == nil && bytes.Equal(scratch.master, a.vault.master)
		clear(plain)
		clearScratch(scratch)
		if !readable {
			continue
		}
		backups = append(backups, backup{path, info})
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].info.ModTime().Equal(backups[j].info.ModTime()) {
			return backups[i].path < backups[j].path
		}
		return backups[i].info.ModTime().After(backups[j].info.ModTime())
	})
	for index := maxRetainedMigrationBackups; index < len(backups); index++ {
		item := backups[index]
		info, err := os.Lstat(item.path)
		if err != nil || !os.SameFile(info, item.info) || regular(item.path) != nil {
			return ErrUnsafePath
		}
		if operations.removeRoot(item.path) != nil || syncDirectory(filepath.Dir(item.path)) != nil {
			return ErrState
		}
	}
	return nil
}

func knownRootInaccessibleWithOSKey(v *Vault, data []byte) bool {
	if bytes.HasPrefix(data, rootHeader) {
		root, err := decodeRoot(data[len(rootHeader):])
		if err != nil {
			return false
		}
		key, err := keymaterial.Derive(v.key, "os-master-wrap/v2")
		if err != nil {
			return false
		}
		aead, _ := rootAEAD(key)
		clear(key)
		aad := append(append([]byte(nil), rootHeader...), []byte("os-only master slot")...)
		if root.Mode == "os-plus-passphrase" {
			aad = passphraseAAD(root.Slots.Passphrase, "os-layer")
		}
		plain, err := aead.Open(nil, root.Slots.OS.Nonce, root.Slots.OS.Ciphertext, aad)
		clear(plain)
		return err != nil
	}
	if bytes.HasPrefix(data, header) {
		aead, err := v.aead()
		if err != nil || len(data) < len(header)+aead.NonceSize()+aead.Overhead() {
			return false
		}
		plain, err := aead.Open(nil, data[len(header):len(header)+aead.NonceSize()], data[len(header)+aead.NonceSize():], header)
		clear(plain)
		return err != nil
	}
	return false
}

func (a *ApplicationState) SetPassphrase(ctx context.Context, current, next []byte) error {
	return a.setPassphraseWithIO(ctx, current, next, managedCopyIO{})
}

func (a *ApplicationState) setPassphraseWithIO(ctx context.Context, current, next []byte, operations managedCopyIO) error {
	if a == nil || a.vault == nil || ctx == nil {
		return ErrLocked
	}
	if len(next) > 0 && keymaterial.ValidateSecret(next) != nil || len(current) > 0 && keymaterial.ValidateSecret(current) != nil {
		return ErrPassphrase
	}
	v := a.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := a.usableLocked(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if v.protectionMode == "os-plus-passphrase" {
		data, err := readCipherFile(filepath.Join(v.directory, "state.vault"), maxRootFileBytes)
		if err != nil {
			return err
		}
		scratch := scratchVault(v)
		plain, err := decodeRootWithSecret(scratch, data, current)
		clear(plain)
		same := bytes.Equal(scratch.master, v.master)
		clearScratch(scratch)
		if errors.Is(err, ErrPassphrase) || errors.Is(err, ErrPassphraseRequired) {
			return ErrPassphrase
		}
		if err != nil {
			return err
		}
		if !same {
			return ErrCorrupt
		}
	}
	mode := "os-only"
	var salt, key []byte
	var err error
	if len(next) > 0 {
		mode = "os-plus-passphrase"
		salt, err = keymaterial.PassphraseSalt()
		if err != nil {
			return err
		}
		key, err = derivePassphrase(next, salt)
		if err != nil {
			clear(salt)
			return err
		}
	}
	oldMode, oldSalt, oldKey := v.protectionMode, v.passSalt, v.passKey
	v.protectionMode, v.passSalt, v.passKey = mode, salt, key
	defer func() { clear(oldSalt); clear(oldKey) }()
	if ctx.Err() != nil {
		v.protectionMode, v.passSalt, v.passKey = oldMode, oldSalt, oldKey
		clear(salt)
		clear(key)
		oldSalt = nil
		oldKey = nil
		return ctx.Err()
	}
	finished := false
	defer func() {
		if !finished {
			a.failed = true
		}
	}()
	state := a.cloneLocked()
	if mode == "os-plus-passphrase" {
		state.MigrationBackup = ""
		state.MigrationBackupHash = ""
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return ErrState
	}
	defer clear(payload)
	candidate, err := v.sealRootLocked(payload)
	if err != nil {
		return err
	}
	scratch := scratchVault(v)
	scratch.passSalt = bytes.Clone(salt)
	scratch.passKey = bytes.Clone(key)
	verified, err := scratch.loadRootLocked(candidate[len(rootHeader):])
	same := bytes.Equal(verified, payload) && bytes.Equal(scratch.master, v.master)
	clear(verified)
	clearScratch(scratch)
	if err != nil || !same {
		a.failed = true
		return ErrState
	}
	if mode == "os-plus-passphrase" {
		if err := protectManagedCopiesWithIOLocked(v, operations, current); err != nil {
			a.failed = true
			return err
		}
	}
	if err := a.commitLocked(state); err != nil {
		return err
	}
	installed, err := readCipherFile(filepath.Join(v.directory, "state.vault"), maxRootFileBytes)
	if err != nil {
		a.failed = true
		return err
	}
	scratch = scratchVault(v)
	defer clearScratch(scratch)
	verified, err = decodeRootWithSecret(scratch, installed, next)
	same = bytes.Equal(verified, payload) && bytes.Equal(scratch.master, v.master)
	clear(verified)
	if err != nil || !same {
		a.failed = true
		return ErrState
	}
	finished = true
	// Disabling first commits the OS-only root. An interruption must not expose
	// an OS-only backup while the authoritative root still requires a passphrase.
	// Retained protected copies are safe to reconcile after that commit and again
	// on the next authenticated open. Pruning is never part of commit success.
	if mode == "os-only" {
		_ = protectManagedCopiesWithIOLocked(v, operations, current)
	}
	_ = a.pruneManagedCopiesLocked(operations)
	return nil
}
