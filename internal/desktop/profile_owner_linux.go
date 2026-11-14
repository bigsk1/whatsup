//go:build linux

package desktop

import (
	"os"
	"syscall"
)

func profileDirectoryOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
