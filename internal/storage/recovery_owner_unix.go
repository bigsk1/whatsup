//go:build !windows

package storage

import (
	"os"
	"syscall"
)

func recoveryOwned(info os.FileInfo) bool {
	value, ok := info.Sys().(*syscall.Stat_t)
	return ok && value.Uid == uint32(os.Getuid())
}
