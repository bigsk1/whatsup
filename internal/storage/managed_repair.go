package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"whatsup/internal/keymaterial"
)

// SetAsideManagedCopy is a backend-only repair boundary. The desktop supplies
// the exact name/digest from its last authenticated-open failure, never renderer
// paths. Existing OS credentials and the authoritative root's app factor are
// required again. The original bytes stay encrypted in place, behind the master.
func SetAsideManagedCopy(ctx context.Context, directory string, keys KeyStore, secret []byte, relative string, digest [32]byte) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrLocked
	}
	if len(secret) > 0 && keymaterial.ValidateSecret(secret) != nil {
		return ErrPassphrase
	}
	if digest == [32]byte{} || relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return ErrUnsafePath
	}
	// The repair path cannot reconcile first: that is precisely where the
	// captured damaged copy may stop a pending install. It cannot mint keys.
	v, err := openWithSecretPolicy(directory, keys, secret, false)
	if err != nil {
		return err
	}
	defer v.Close()
	paths, err := managedRootPaths(v)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, relative)
	allowed := false
	for _, candidate := range paths {
		if candidate == path {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrUnsafePath
	}
	rootPath := filepath.Join(directory, "state.vault")
	if _, err := os.Lstat(filepath.Join(directory, recoveryJournalFilename)); err == nil {
		journal, err := readRecoveryJournal(directory, v.key)
		if err != nil {
			return err
		}
		// Only the exact installed or staged NEW root can grant authority for a
		// pending replacement. Never authenticate the retiring root instead.
		matches := func(candidate string) bool {
			data, err := readCipherFile(candidate, maxRootFileBytes)
			if err != nil {
				return false
			}
			hash := sha256.Sum256(data)
			return hex.EncodeToString(hash[:]) == journal.RootHash
		}
		if !matches(rootPath) {
			stage := filepath.Join(directory, ".recovery-stage-"+journal.StageID)
			if privateRecoveryDirectory(stage) != nil {
				return ErrUnsafePath
			}
			rootPath = filepath.Join(stage, "state.vault")
			if !matches(rootPath) {
				return ErrRecoveryPending
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if path == rootPath {
		return ErrUnsafePath
	}
	root, err := readCipherFile(rootPath, maxRootFileBytes)
	if err != nil {
		return err
	}
	plain, err := decodeRootWithSecret(v, root, secret)
	clear(plain)
	if err != nil {
		return err
	}
	if len(v.master) != 32 {
		return ErrCorrupt
	}
	data, err := readCipherFile(path, maxManagedRootBytes)
	if err != nil {
		return err
	}
	actual := sha256.Sum256(data)
	if !bytes.Equal(actual[:], digest[:]) {
		return ErrState
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return preserveManagedRoot(v, path, data, managedCopyIO{}, managedCopyPolicy{preserveOpaque: true, activeRoot: rootPath}, ErrCorrupt)
}
