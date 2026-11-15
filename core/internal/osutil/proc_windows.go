//go:build windows

package osutil

import (
	"os"
	"os/exec"
	"syscall"
)

// ProcessAlive reports whether a process with this id exists. On Windows
// FindProcess opens the process and fails when there is none.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	p.Release()
	return true
}

// Detach makes cmd start without a console and in its own process group,
// so it survives the process that launched it.
func Detach(cmd *exec.Cmd) {
	const detachedProcess, createNewProcessGroup = 0x00000008, 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
}
