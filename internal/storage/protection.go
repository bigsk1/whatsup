package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"whatsup/internal/keymaterial"
)

type ProtectionStatus struct {
	Exists             bool `json:"exists"`
	RequiresPassphrase bool `json:"requiresPassphrase"`
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

// Only fixed app-owned root copies are considered. Protected historical copies
// remain protected; an OS-only copy is rewritten before publishing protection.
func protectManagedCopiesLocked(v *Vault, currentSecrets ...[]byte) error {
	entries, err := os.ReadDir(v.directory)
	if err != nil {
		return err
	}
	var paths []string
	for _, e := range entries {
		if ownedRootTemporaryName.MatchString(e.Name()) {
			path := filepath.Join(v.directory, e.Name())
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || !recoveryOwned(info) || info.Size() < 0 || info.Size() > maxRootFileBytes || regular(path) != nil {
				return ErrUnsafePath
			}
			if os.Remove(path) != nil {
				return ErrState
			}
			continue
		}
		if migrationBackupName.MatchString(e.Name()) {
			paths = append(paths, filepath.Join(v.directory, e.Name()))
		}
		if recoveryRollbackName.MatchString(e.Name()) || recoveryStageName.MatchString(e.Name()) {
			if privateRecoveryDirectory(filepath.Join(v.directory, e.Name())) != nil {
				return ErrUnsafePath
			}
			paths = append(paths, filepath.Join(v.directory, e.Name(), "state.vault"))
		}
	}
	for _, path := range paths {
		data, err := readCipherFile(path, maxRootFileBytes)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if bytes.HasPrefix(data, rootHeader) {
			root, err := decodeRoot(data[len(rootHeader):])
			if err != nil {
				return err
			}
			if root.Mode == "os-plus-passphrase" {
				if rootUsesCurrentProtection(v, root) {
					continue
				}
				sealed, same, err := rewrapSameMasterRoot(v, data)
				if err != nil {
					return err
				}
				if same {
					if err := atomicCipherReplace(path, sealed); err != nil {
						return err
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
						if err := atomicCipherReplace(path, sealed); err != nil {
							return err
						}
					} else {
						clear(plain)
						clearScratch(old)
					}
				}
				continue
			}
		}
		scratch := scratchVault(v)
		plain, err := decodeRootWithSecret(scratch, data, nil)
		if err != nil {
			if knownRootInaccessibleWithOSKey(scratch, data) {
				clearScratch(scratch)
				continue
			}
			clearScratch(scratch)
			return err
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
		if err := atomicCipherReplace(path, sealed); err != nil {
			return err
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
		if err != nil || !same {
			return ErrPassphrase
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
	{
		if err := protectManagedCopiesLocked(v, current); err != nil {
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
	return nil
}
