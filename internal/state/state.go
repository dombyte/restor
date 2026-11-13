// Package state records which services a job stopped until it started them again. A run
// that ends in between (killed after its shutdown deadline, out of memory, a power loss)
// leaves the record behind, and the next run starts those services before anything else:
// without it they would stay down, and the next backup would find nothing to stop.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/dombyte/restor/internal/util"
)

const (
	// rootDir keeps root's records across reboots: a container stopped with
	// `compose stop` stays stopped after a reboot, also with restart: unless-stopped.
	rootDir = "/var/lib/restor"

	dirMode  fs.FileMode = 0o700
	fileMode fs.FileMode = 0o600
	// writableByOthers are the mode bits that let other users plant or remove records.
	writableByOthers fs.FileMode = 0o022
)

var (
	// ErrMissingDependency is returned by New for a missing setting.
	ErrMissingDependency = errors.New("state: missing dependency")
	// ErrUnsafeDir is returned when the state directory is a symlink, belongs to another
	// user or is writable by others.
	ErrUnsafeDir = errors.New("state: unsafe state directory")
)

// User describes the user a state directory is chosen for.
type User struct {
	// UID is the user ID of the running process.
	UID int
	// StateHome is $XDG_STATE_HOME; ignored unless absolute.
	StateHome string
	// Home is $HOME; ignored unless absolute.
	Home string
	// Fallback is used without an absolute StateHome or Home (the lock directory: its
	// records do not survive a reboot, but a crash without one).
	Fallback string
}

// DefaultDir returns the state directory for u: /var/lib/restor for root, otherwise
// restor in $XDG_STATE_HOME or ~/.local/state, else Fallback.
func DefaultDir(u User) string {
	switch {
	case u.UID == 0:
		return rootDir
	case filepath.IsAbs(u.StateHome):
		return filepath.Join(u.StateHome, "restor")
	case filepath.IsAbs(u.Home):
		return filepath.Join(u.Home, ".local", "state", "restor")
	default:
		return u.Fallback
	}
}

// Settings configure a Store.
type Settings struct {
	// Dir holds the records; it is created on demand.
	Dir string
}

// Store keeps one record per job in a directory.
type Store struct {
	dir string
	uid int
}

// record is the content of a record file.
type record struct {
	Job      string   `json:"job"`
	Services []string `json:"services"`
}

// New returns a Store.
func New(s Settings) (*Store, error) {
	if err := util.RequireAll(ErrMissingDependency,
		util.Requirement{Name: "Dir", OK: s.Dir != ""},
	); err != nil {
		return nil, err
	}
	return &Store{dir: s.Dir, uid: os.Getuid()}, nil
}

// Save records that job stopped services. The file is replaced atomically and synced, so
// a crash leaves either the old record or the new one.
func (s *Store) Save(job string, services []string) error {
	if err := s.ensureDir(); err != nil {
		return err
	}
	data, err := json.Marshal(record{Job: job, Services: services})
	if err != nil {
		return fmt.Errorf("state: %s: %w", job, err)
	}
	tmp, err := os.CreateTemp(s.dir, ".record-*")
	if err != nil {
		return fmt.Errorf("state: %s: %w", job, err)
	}
	// Gone after the rename; otherwise a leftover temporary file is harmless.
	defer os.Remove(tmp.Name())
	if err := writeSynced(tmp, data); err != nil {
		return fmt.Errorf("state: %s: %w", job, err)
	}
	if err := os.Rename(tmp.Name(), s.path(job)); err != nil {
		return fmt.Errorf("state: %s: %w", job, err)
	}
	return s.syncDir()
}

// writeSynced writes data to f, syncs and closes it.
func writeSynced(f *os.File, data []byte) error {
	_, err := f.Write(data)
	if err == nil {
		err = f.Chmod(fileMode)
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// syncDir makes the rename durable.
func (s *Store) syncDir() error {
	d, err := os.Open(s.dir)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	return errors.Join(d.Sync(), d.Close())
}

// Load returns the services that job stopped and did not start again; none without a
// record.
func (s *Store) Load(job string) ([]string, error) {
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path(job))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: %s: %w", job, err)
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("state: %s: %s: %w", job, s.path(job), err)
	}
	return r.Services, nil
}

// Remove deletes the record of job; a missing one is not an error.
func (s *Store) Remove(job string) error {
	if err := os.Remove(s.path(job)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("state: %s: %w", job, err)
	}
	return nil
}

// Path returns the record file of job, for messages that tell the user where it is.
func (s *Store) Path(job string) string { return s.path(job) }

// path maps a job name to its file; hashing keeps any name a plain file name.
func (s *Store) path(job string) string {
	sum := sha256.Sum256([]byte(job))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".json")
}

// ensureDir creates the state directory and checks that only this user can change it.
func (s *Store) ensureDir() error {
	if err := os.MkdirAll(s.dir, dirMode); err != nil {
		return fmt.Errorf("state: create %s: %w", s.dir, err)
	}
	info, err := os.Lstat(s.dir)
	if err != nil {
		return fmt.Errorf("state: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&writableByOthers != 0 {
		return fmt.Errorf("%w: %s (mode %s)", ErrUnsafeDir, s.dir, info.Mode())
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int64(st.Uid) != int64(s.uid) {
		return fmt.Errorf("%w: %s is owned by uid %d", ErrUnsafeDir, s.dir, st.Uid)
	}
	return nil
}
