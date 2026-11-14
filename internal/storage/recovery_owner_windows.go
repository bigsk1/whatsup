//go:build windows

package storage

import "os"

func recoveryOwned(os.FileInfo) bool { return true }
