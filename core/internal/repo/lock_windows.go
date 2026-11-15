//go:build windows

package repo

import "os"

// processAlive reports whether a process with this id exists. On Windows
// FindProcess opens the process and fails when there is none.
func processAlive(pid int) bool {
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
