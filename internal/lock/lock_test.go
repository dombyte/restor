package lock

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLocker(t *testing.T) *Locker {
	t.Helper()
	l, err := New(Settings{Dir: filepath.Join(t.TempDir(), "locks")})
	require.NoError(t, err)
	return l
}

func TestNew_MissingDependencies(t *testing.T) {
	t.Parallel()
	_, err := New(Settings{})
	require.ErrorIs(t, err, ErrMissingDependency)
}

func TestLocker_LockAndRelease(t *testing.T) {
	t.Parallel()
	l := newLocker(t)

	release, err := l.Lock("/srv/app/compose.yml")
	require.NoError(t, err)

	data, err := os.ReadFile(l.path("/srv/app/compose.yml"))
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid())+"\n/srv/app/compose.yml\n", string(data))

	require.NoError(t, release())
	require.NoError(t, release(), "releasing twice is harmless")

	release, err = l.Lock("/srv/app/compose.yml")
	require.NoError(t, err, "a released lock can be taken again")
	require.NoError(t, release())
}

func TestLocker_LockHeld(t *testing.T) {
	t.Parallel()
	l := newLocker(t)
	release, err := l.Lock("units")
	require.NoError(t, err)
	defer func() { require.NoError(t, release()) }()

	_, err = l.Lock("units")

	require.ErrorIs(t, err, ErrLocked)
}

func TestLocker_LockIgnoresLeftoverFile(t *testing.T) {
	t.Parallel()
	l := newLocker(t)
	require.NoError(t, os.MkdirAll(l.dir, dirMode))
	// A file left by a crashed run (or an earlier version): nobody holds a flock on it.
	require.NoError(t, os.WriteFile(l.path("units"), []byte("4242\nold content\n"), fileMode))

	release, err := l.Lock("units")

	require.NoError(t, err)
	data, err := os.ReadFile(l.path("units"))
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid())+"\nunits\n", string(data))
	require.NoError(t, release())
}

func TestLocker_DistinctKeysDoNotCollide(t *testing.T) {
	t.Parallel()
	l := newLocker(t)
	long := strings.Repeat("unit.service,", 100) // longer than a file name may be
	var releases []func() error
	for _, key := range []string{"/srv/a_b.yml", "/srv/a/b.yml", "/srv/a.b.yml", long} {
		release, err := l.Lock(key)
		require.NoError(t, err, key)
		releases = append(releases, release)
	}
	for _, release := range releases {
		require.NoError(t, release())
	}
}

func TestLocker_LockRefusesSymlinkedFile(t *testing.T) {
	t.Parallel()
	l := newLocker(t)
	require.NoError(t, os.MkdirAll(l.dir, dirMode))
	target := filepath.Join(t.TempDir(), "victim")
	require.NoError(t, os.WriteFile(target, []byte("keep"), fileMode))
	require.NoError(t, os.Symlink(target, l.path("units")))

	_, err := l.Lock("units")

	require.Error(t, err)
	data, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
}

func TestLocker_LockDirFails(t *testing.T) {
	t.Parallel()
	l := newLocker(t)
	require.NoError(t, os.WriteFile(l.dir, nil, fileMode)) // a file where the dir belongs

	_, err := l.Lock("units")

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrLocked)
}

func TestLocker_LockRefusesWritableDir(t *testing.T) {
	t.Parallel()
	l := newLocker(t)
	require.NoError(t, os.MkdirAll(l.dir, dirMode))
	require.NoError(t, os.Chmod(l.dir, 0o777))

	_, err := l.Lock("units")

	require.ErrorIs(t, err, ErrUnsafeDir)
}

func TestLocker_LockRefusesSymlinkedDir(t *testing.T) {
	t.Parallel()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "locks")
	require.NoError(t, os.Symlink(real, link))
	l, err := New(Settings{Dir: link})
	require.NoError(t, err)

	_, err = l.Lock("units")

	require.ErrorIs(t, err, ErrUnsafeDir)
}

func TestDefaultDir(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		user User
		want string
	}{
		"root on linux": {
			User{UID: 0, GOOS: "linux", RuntimeDir: "/run/user/1000", TempDir: "/tmp"},
			"/run/restor",
		},
		"user with runtime dir": {
			User{UID: 1000, GOOS: "linux", RuntimeDir: "/run/user/1000", TempDir: "/tmp"},
			"/run/user/1000/restor",
		},
		"user without runtime dir": {
			User{UID: 1000, GOOS: "linux", TempDir: "/tmp"},
			"/tmp/restor-1000",
		},
		"root on darwin": {
			User{UID: 0, GOOS: "darwin", TempDir: "/var/folders/x/T"},
			"/var/folders/x/T/restor-0",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, DefaultDir(tt.user))
		})
	}
}
