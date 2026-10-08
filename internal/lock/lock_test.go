package lock

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/lock/mocks"
	"github.com/dombyte/restor/internal/util/clocktest"
)

var start = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

func newLocker(t *testing.T) (*Locker, *mocks.MockProcesses, *clocktest.Clock) {
	t.Helper()
	procs := mocks.NewMockProcesses(t)
	clock := clocktest.New(start)
	l, err := New(Settings{Dir: filepath.Join(t.TempDir(), "locks")},
		Deps{Clock: clock, Processes: procs, Log: zerolog.Nop()})
	require.NoError(t, err)
	return l, procs, clock
}

func writeLock(t *testing.T, l *Locker, key, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(l.dir, dirMode))
	path := l.path(key)
	require.NoError(t, os.WriteFile(path, []byte(content), fileMode))
	return path
}

func TestNew_MissingDependencies(t *testing.T) {
	t.Parallel()
	_, err := New(Settings{}, Deps{})
	require.ErrorIs(t, err, ErrMissingDependency)
}

func TestLocker_LockAndRelease(t *testing.T) {
	t.Parallel()
	l, _, _ := newLocker(t)

	release, err := l.Lock("/srv/app/compose.yml")
	require.NoError(t, err)

	path := filepath.Join(l.dir, "_srv_app_compose_yml.lock")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(os.Getpid())+"\n"+strconv.FormatInt(start.Unix(), 10),
		string(data))

	require.NoError(t, release())
	assert.NoFileExists(t, path)
	require.NoError(t, release(), "releasing twice is harmless")
}

func TestLocker_LockHeldByLiveProcess(t *testing.T) {
	t.Parallel()
	l, procs, _ := newLocker(t)
	writeLock(t, l, "units", "4242\n1")
	procs.EXPECT().Alive(4242).Return(true).Once()

	_, err := l.Lock("units")

	require.ErrorIs(t, err, ErrLocked)
}

func TestLocker_LockReplacesStaleLock(t *testing.T) {
	t.Parallel()
	l, procs, _ := newLocker(t)
	path := writeLock(t, l, "units", "4242\n1")
	procs.EXPECT().Alive(4242).Return(false).Once()

	release, err := l.Lock("units")

	require.NoError(t, err)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), strconv.Itoa(os.Getpid()))
	require.NoError(t, release())
}

func TestLocker_LockUnreadable(t *testing.T) {
	t.Parallel()
	l, _, clock := newLocker(t)
	path := writeLock(t, l, "units", "garbage")
	require.NoError(t, os.Chtimes(path, start, start))

	_, err := l.Lock("units")
	require.ErrorIs(t, err, ErrLocked, "a fresh unreadable lock is respected")

	clock.After(2 * unreadableGrace)
	release, err := l.Lock("units")
	require.NoError(t, err, "an old unreadable lock is stale")
	require.NoError(t, release())
}

func TestLocker_LockDirFails(t *testing.T) {
	t.Parallel()
	l, _, _ := newLocker(t)
	require.NoError(t, os.WriteFile(l.dir, nil, fileMode)) // a file where the dir belongs

	_, err := l.Lock("units")

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrLocked)
}

func TestLocker_CleanupStale(t *testing.T) {
	t.Parallel()
	l, procs, _ := newLocker(t)
	live := writeLock(t, l, "live", "1\n1")
	dead := writeLock(t, l, "dead", "2\n1")
	procs.EXPECT().Alive(1).Return(true).Once()
	procs.EXPECT().Alive(2).Return(false).Once()

	require.NoError(t, l.CleanupStale())

	assert.FileExists(t, live)
	assert.NoFileExists(t, dead)
}

func TestLocker_CleanupStaleNoDir(t *testing.T) {
	t.Parallel()
	l, _, _ := newLocker(t)
	require.NoError(t, l.CleanupStale())
}

func TestLocker_CleanupStaleUnreadableFile(t *testing.T) {
	t.Parallel()
	l, procs, _ := newLocker(t)
	require.NoError(t, os.MkdirAll(filepath.Join(l.dir, "dir.lock"), dirMode))
	procs.AssertNotCalled(t, "Alive", mock.Anything)

	require.Error(t, l.CleanupStale(), "a directory cannot be read as a lock file")
}

func TestLocker_Path(t *testing.T) {
	t.Parallel()
	l := &Locker{dir: "/locks"}
	tests := map[string]string{
		"/srv/app/docker-compose.yml": "/locks/_srv_app_docker-compose_yml.lock",
		"a.service,b.service":         "/locks/a_service,b_service.lock",
		`C:\x`:                        "/locks/C__x.lock",
	}
	for key, want := range tests {
		assert.Equal(t, want, l.path(key), key)
	}
}

func TestOSProcesses_Alive(t *testing.T) {
	t.Parallel()
	p := OSProcesses{}
	assert.True(t, p.Alive(os.Getpid()))
	assert.False(t, p.Alive(0))
	assert.False(t, p.Alive(-1))
	assert.False(t, p.Alive(1<<22+12345), "PIDs above the Linux maximum do not exist")
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

func TestLocker_LockRefusesWritableDir(t *testing.T) {
	t.Parallel()
	l, _, _ := newLocker(t)
	require.NoError(t, os.MkdirAll(l.dir, dirMode))
	require.NoError(t, os.Chmod(l.dir, 0o777))

	_, err := l.Lock("units")

	require.ErrorIs(t, err, ErrUnsafeDir)
}
