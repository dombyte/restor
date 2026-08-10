package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// Locker manages per-project locks to prevent concurrent backups of the same compose file
type Locker struct {
	lockDir string
}

// NewLocker creates a new Locker with the specified lock directory
func NewLocker(lockDir string) *Locker {
	if lockDir == "" {
		lockDir = "/tmp/dbk-locks"
	}
	return &Locker{
		lockDir: lockDir,
	}
}

// lockFileName generates a lock file name from compose file path
func (l *Locker) lockFileName(composeFile string) string {
	// Create a filesystem-safe name from the compose file path
	// Replace path separators and special characters
	safeName := strings.ReplaceAll(composeFile, "/", "_")
	safeName = strings.ReplaceAll(safeName, "\\", "_")
	safeName = strings.ReplaceAll(safeName, ":", "_")
	safeName = strings.ReplaceAll(safeName, ".", "_")

	// Ensure lock directory exists
	if err := os.MkdirAll(l.lockDir, 0755); err != nil {
		log.Error().Err(err).Str("lock_dir", l.lockDir).Msg("Failed to create lock directory")
		return ""
	}

	return filepath.Join(l.lockDir, safeName+".lock")
}

// Lock acquires a lock for the specified compose file
// Returns true if lock was acquired, false if already locked
func (l *Locker) Lock(composeFile string) (bool, error) {
	lockFile := l.lockFileName(composeFile)
	if lockFile == "" {
		return false, fmt.Errorf("failed to create lock file name")
	}

	// Check if lock file already exists and is valid
	if l.IsLocked(composeFile) {
		return false, nil
	}

	// Create lock file with current PID
	pid := os.Getpid()
	lockContent := fmt.Sprintf("%d\n%d", pid, time.Now().Unix())

	if err := os.WriteFile(lockFile, []byte(lockContent), 0644); err != nil {
		return false, fmt.Errorf("failed to create lock file: %w", err)
	}

	log.Debug().Str("compose_file", composeFile).Int("pid", pid).Msg("Lock acquired")
	return true, nil
}

// Unlock releases the lock for the specified compose file
func (l *Locker) Unlock(composeFile string) error {
	lockFile := l.lockFileName(composeFile)
	if lockFile == "" {
		return fmt.Errorf("failed to create lock file name")
	}

	// Remove lock file
	if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove lock file: %w", err)
	}

	log.Debug().Str("compose_file", composeFile).Msg("Lock released")
	return nil
}

// IsLocked checks if the specified compose file is currently locked
func (l *Locker) IsLocked(composeFile string) bool {
	lockFile := l.lockFileName(composeFile)
	if lockFile == "" {
		return true // Consider locked if we can't determine
	}

	// Check if lock file exists
	if _, err := os.Stat(lockFile); os.IsNotExist(err) {
		return false
	}

	// Check if the process that created the lock is still running
	return l.isLockStale(lockFile)
}

// isLockStale checks if the lock is stale (process no longer running)
func (l *Locker) isLockStale(lockFile string) bool {
	// Read lock file
	data, err := os.ReadFile(lockFile)
	if err != nil {
		return false
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 1 {
		return false
	}

	// Parse PID from first line
	var pid int
	if _, err := fmt.Sscanf(lines[0], "%d", &pid); err != nil {
		return false
	}

	// Check if process exists
	if l.isProcessRunning(pid) {
		return true // Lock is valid, process is running
	}

	// Process is not running, lock is stale - remove it
	log.Warn().Int("pid", pid).Str("lock_file", lockFile).Msg("Removing stale lock file")
	os.Remove(lockFile)
	return false
}

// isProcessRunning checks if a process with the given PID is running
func (l *Locker) isProcessRunning(pid int) bool {
	// Check if /proc/<pid> exists (Linux)
	procPath := fmt.Sprintf("/proc/%d", pid)
	if _, err := os.Stat(procPath); err == nil {
		return true
	}

	// For non-Linux systems, try sending signal 0 (which doesn't actually send a signal)
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(os.Signal(nil))
	return err == nil
}

// CleanupStaleLocks removes all stale lock files
func (l *Locker) CleanupStaleLocks() error {
	// List all lock files
	files, err := filepath.Glob(filepath.Join(l.lockDir, "*.lock"))
	if err != nil {
		return err
	}

	for _, lockFile := range files {
		if !l.isLockStale(lockFile) {
			// Lock is still valid, skip
			continue
		}
		// Lock is stale, remove it
		log.Info().Str("lock_file", lockFile).Msg("Cleaned up stale lock file")
		if err := os.Remove(lockFile); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove stale lock file %s: %w", lockFile, err)
		}
	}

	return nil
}
