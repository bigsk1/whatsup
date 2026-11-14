package storage

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func privateRecoveryDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !recoveryOwned(info) || runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return ErrUnsafePath
	}
	return nil
}

func removeRecoveryStage(directory, id string) error {
	if id == "" {
		return nil
	}
	if !filepath.IsAbs(directory) || !pendingIDValid(id) {
		return ErrUnsafePath
	}
	target := filepath.Join(directory, ".recovery-stage-"+id)
	absolute, err := filepath.Abs(target)
	if err != nil || filepath.Dir(absolute) != filepath.Clean(directory) || !strings.HasPrefix(filepath.Base(absolute), ".recovery-stage-") {
		return ErrUnsafePath
	}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return nil
	}
	if privateRecoveryDirectory(target) != nil {
		return ErrUnsafePath
	}
	// Validate every target without following links before a bounded Go-owned
	// staging directory is recursively removed. No user-provided archive path
	// participates in this operation.
	if err := filepath.WalkDir(target, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrUnsafePath
		}
		if entry.IsDir() {
			return privateRecoveryDirectory(path)
		}
		return regular(path)
	}); err != nil {
		return err
	}
	return os.RemoveAll(target)
}
