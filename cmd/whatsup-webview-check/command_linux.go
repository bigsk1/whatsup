//go:build linux && cgo && !gtk3 && !purego && !android && !server

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func prepareNativeCommand(command *exec.Cmd) {
	// Cancel only this probe's new process group. Ordinary GUI/application
	// processes are never selected or signaled by the developer probe.
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
