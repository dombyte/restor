package lock

import (
	"errors"
	"os"
	"syscall"
)

// OSProcesses checks processes of the running system.
type OSProcesses struct{}

// Alive reports whether a process with pid exists (signal 0; EPERM means it exists but
// belongs to another user).
func (OSProcesses) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
