//go:build !windows

package osutil

import (
	"errors"
	"os/exec"
	"syscall"
)

// ProcessAlive reports whether a process with this id exists.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// Detach makes cmd start in its own session, so it survives the process
// that launched it and receives none of its signals.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
