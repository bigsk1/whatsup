//go:build linux

package desktop

import (
	"os"
	"syscall"
	"testing"
)

type profileOwnerInfo struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (info profileOwnerInfo) Sys() any { return info.stat }

func TestProfileDirectoryRequiresCurrentLinuxOwner(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !profileDirectoryOwnedByCurrentUser(info) {
		t.Fatal("rejected current user directory")
	}
	foreign := profileOwnerInfo{FileInfo: info, stat: &syscall.Stat_t{Uid: uint32(os.Geteuid()) ^ 1}}
	if profileDirectoryOwnedByCurrentUser(foreign) {
		t.Fatal("accepted another user's private directory")
	}
}
