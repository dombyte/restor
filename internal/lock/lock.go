// Package lock provides per-key lock files that keep two runs (two processes, or two
// projects of one run) from stopping and backing up the same services at the same time.
// A lock file holds the PID of its owner and is valid while that process is alive.
package lock

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/dombyte/restor/internal/util"
)

const (
	// rootDir holds root's lock files on Linux: /run is a root-only tmpfs.
	rootDir = "/run/restor"
	// unreadableGrace is how long an unreadable lock file (e.g. just being written by
	// another process) is respected before it counts as stale.
	unreadableGrace = time.Minute

	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
	// writableByOthers are the mode bits that let other users plant or remove locks.
	writableByOthers fs.FileMode = 0o022
)

var (
	// ErrMissingDependency is returned by New for a missing dependency or setting.
	ErrMissingDependency = errors.New("lock: missing dependency")
	// ErrLocked is returned by Lock when a live process holds the lock.
	ErrLocked = errors.New("lock: already locked")
	// ErrUnsafeDir is returned by Lock when the lock directory is a symlink, belongs to
	// another user or is writable by others.
	ErrUnsafeDir = errors.New("lock: unsafe lock directory")
)

// User describes the user a lock directory is chosen for.
type User struct {
	// UID is the user ID of the running process.
	UID int
	// GOOS is the operating system (runtime.GOOS).
	GOOS string
	// RuntimeDir is $XDG_RUNTIME_DIR; empty when unset.
	RuntimeDir string
	// TempDir is the temporary directory (os.TempDir).
	TempDir string
}

// DefaultDir returns the lock directory for u: /run/restor for root on Linux, otherwise
// restor in the user's runtime dir, or restor-<uid> in the temp dir without one. Each
// user gets their own directory, so runs of different users never block each other on
// file permissions.
func DefaultDir(u User) string {
	switch {
	case u.UID == 0 && u.GOOS == "linux":
		return rootDir
	case u.RuntimeDir != "":
		return filepath.Join(u.RuntimeDir, "restor")
	default:
		return filepath.Join(u.TempDir, "restor-"+strconv.Itoa(u.UID))
	}
}

// Processes reports whether a process exists.
type Processes interface {
	Alive(pid int) bool
}

// Settings configure a Locker.
type Settings struct {
	// Dir holds the lock files; it is created on demand.
	Dir string
}

// Deps are the dependencies of a Locker.
type Deps struct {
	// Clock timestamps lock files and ages unreadable ones.
	Clock util.Clock
	// Processes checks whether a lock owner is still running.
	Processes Processes
	// Log reports removed stale locks.
	Log zerolog.Logger
}

// Locker creates and removes lock files in one directory.
type Locker struct {
	dir   string
	pid   int
	uid   int
	clock util.Clock
	procs Processes
	log   zerolog.Logger
}

// New returns a Locker owned by the current process.
func New(s Settings, d Deps) (*Locker, error) {
	if err := util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Dir", OK: s.Dir != ""},
		util.Requirement{Name: "Clock", OK: d.Clock != nil},
		util.Requirement{Name: "Processes", OK: d.Processes != nil},
	); err != nil {
		return nil, err
	}
	return &Locker{
		dir: s.Dir, pid: os.Getpid(), uid: os.Getuid(),
		clock: d.Clock, procs: d.Processes, log: d.Log,
	}, nil
}

// Lock takes the lock for key and returns the function that releases it. A lock held by
// a live process returns ErrLocked; a stale one is replaced.
func (l *Locker) Lock(key string) (func() error, error) {
	if err := l.ensureDir(); err != nil {
		return nil, err
	}
	path := l.path(key)
	err := l.create(path)
	if errors.Is(err, fs.ErrExist) {
		if removed, staleErr := l.removeIfStale(path); staleErr != nil {
			return nil, staleErr
		} else if removed {
			err = l.create(path)
		}
	}
	if errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("%w: %s", ErrLocked, key)
	}
	if err != nil {
		return nil, fmt.Errorf("lock: %s: %w", key, err)
	}
	return func() error { return remove(path) }, nil
}

// CleanupStale removes the lock files of processes that no longer run.
func (l *Locker) CleanupStale() error {
	files, err := filepath.Glob(filepath.Join(l.dir, "*.lock"))
	if err != nil {
		return fmt.Errorf("lock: list %s: %w", l.dir, err)
	}
	var errs []error
	for _, f := range files {
		if _, err := l.removeIfStale(f); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ensureDir creates the lock directory and checks that only this user can change it; a
// directory in the shared temp dir may have been created by someone else.
func (l *Locker) ensureDir() error {
	if err := os.MkdirAll(l.dir, dirMode); err != nil {
		return fmt.Errorf("lock: create %s: %w", l.dir, err)
	}
	info, err := os.Lstat(l.dir)
	if err != nil {
		return fmt.Errorf("lock: %s: %w", l.dir, err)
	}
	if !info.IsDir() || info.Mode().Perm()&writableByOthers != 0 {
		return fmt.Errorf("%w: %s (mode %s)", ErrUnsafeDir, l.dir, info.Mode())
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int64(st.Uid) != int64(l.uid) {
		return fmt.Errorf("%w: %s is owned by uid %d", ErrUnsafeDir, l.dir, st.Uid)
	}
	return nil
}

// create writes a new lock file; it fails with fs.ErrExist when the file exists.
func (l *Locker) create(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fileMode)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintf(f, "%d\n%d", l.pid, l.clock.Now().Unix())
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return errors.Join(werr, remove(path))
	}
	return nil
}

// removeIfStale removes path when its owner is gone and reports whether it did.
func (l *Locker) removeIfStale(path string) (bool, error) {
	stale, err := l.stale(path)
	if err != nil || !stale {
		return false, err
	}
	if err := remove(path); err != nil {
		return false, err
	}
	l.log.Warn().Str("lock_file", path).Msg("removed stale lock")
	return true, nil
}

// stale reports whether the lock file at path belongs to a process that is gone. A file
// that vanished counts as not stale (there is nothing to remove).
func (l *Locker) stale(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("lock: read %s: %w", path, err)
	}
	first, _, _ := strings.Cut(string(data), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return false, nil //nolint:nilerr // vanished or unreadable: leave it alone
		}
		return l.clock.Now().Sub(info.ModTime()) > unreadableGrace, nil
	}
	return !l.procs.Alive(pid), nil
}

// path maps a key (compose file path, unit list, …) to a file name in the lock dir.
func (l *Locker) path(key string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", ":", "_", ".", "_").Replace(key)
	return filepath.Join(l.dir, safe+".lock")
}

func remove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("lock: remove %s: %w", path, err)
	}
	return nil
}
