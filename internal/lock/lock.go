// Package lock provides per-key locks that keep two runs (two processes, or two projects
// of one run) from stopping and backing up the same services at the same time. A lock is
// an flock(2) on a file in the lock directory: the kernel releases it when its holder
// exits, so a crashed run never leaves a stale lock behind.
package lock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/dombyte/restor/internal/util"
)

const (
	// rootDir holds root's lock files on Linux: /run is a root-only tmpfs.
	rootDir = "/run/restor"

	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
	// writableByOthers are the mode bits that let other users plant or remove locks.
	writableByOthers fs.FileMode = 0o022
)

var (
	// ErrMissingDependency is returned by New for a missing setting.
	ErrMissingDependency = errors.New("lock: missing dependency")
	// ErrLocked is returned by Lock when another holder has the lock.
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

// Settings configure a Locker.
type Settings struct {
	// Dir holds the lock files; it is created on demand.
	Dir string
}

// Locker takes locks on files in one directory.
type Locker struct {
	dir string
	pid int
	uid int
}

// New returns a Locker owned by the current process.
func New(s Settings) (*Locker, error) {
	if err := util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Dir", OK: s.Dir != ""},
	); err != nil {
		return nil, err
	}
	return &Locker{dir: s.Dir, pid: os.Getpid(), uid: os.Getuid()}, nil
}

// Lock takes the lock for key and returns the function that releases it. A lock held by
// another process, or by another Lock call of this one, returns ErrLocked.
func (l *Locker) Lock(key string) (func() error, error) {
	if err := l.ensureDir(); err != nil {
		return nil, err
	}
	// O_NOFOLLOW: a planted symlink must not make restor create or truncate another file.
	f, err := os.OpenFile(l.path(key), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, fileMode)
	if err != nil {
		return nil, fmt.Errorf("lock: %s: %w", key, err)
	}
	if err := l.take(f, key); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	released := false
	return func() error {
		if released {
			return nil
		}
		released = true
		// Closing releases the flock. The file stays: removing it would let a waiting
		// process lock the old inode while a third one creates and locks a new file.
		return f.Close()
	}, nil
}

// take locks f without blocking and records the owner in it for humans reading the dir.
func (l *Locker) take(f *os.File, key string) error {
	conn, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("lock: %s: %w", key, err)
	}
	var lockErr error
	if err := conn.Control(func(fd uintptr) {
		lockErr = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
	}); err != nil {
		return fmt.Errorf("lock: %s: %w", key, err)
	}
	if errors.Is(lockErr, syscall.EWOULDBLOCK) {
		return fmt.Errorf("%w: %s", ErrLocked, key)
	}
	if lockErr != nil {
		return fmt.Errorf("lock: %s: %w", key, lockErr)
	}
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("lock: %s: %w", key, err)
	}
	if _, err := fmt.Fprintf(f, "%d\n%s\n", l.pid, key); err != nil {
		return fmt.Errorf("lock: %s: %w", key, err)
	}
	return nil
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

// path maps a key (compose file path, unit list, …) to a file in the lock dir. Hashing
// keeps distinct keys distinct and the name short whatever the key contains.
func (l *Locker) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(l.dir, hex.EncodeToString(sum[:])+".lock")
}
