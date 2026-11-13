package state

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	s, err := New(Settings{Dir: dir})
	require.NoError(t, err)
	return s, dir
}

func TestNew_MissingDir(t *testing.T) {
	t.Parallel()
	_, err := New(Settings{})
	require.ErrorIs(t, err, ErrMissingDependency)
}

func TestStore_SaveLoadRemove(t *testing.T) {
	t.Parallel()
	s, dir := newStore(t)

	got, err := s.Load("web")
	require.NoError(t, err)
	assert.Nil(t, got, "no record")

	require.NoError(t, s.Save("web", []string{"app", "db"}))
	require.NoError(t, s.Save("web/x", []string{"other"}), "any job name is a file name")
	got, err = s.Load("web")
	require.NoError(t, err)
	assert.Equal(t, []string{"app", "db"}, got)

	info, err := os.Stat(s.Path("web"))
	require.NoError(t, err)
	assert.Equal(t, fileMode, info.Mode().Perm())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "no temporary files left")

	require.NoError(t, s.Remove("web"))
	require.NoError(t, s.Remove("web"), "removing twice is fine")
	got, err = s.Load("web")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestStore_BrokenRecord(t *testing.T) {
	t.Parallel()
	s, dir := newStore(t)
	require.NoError(t, os.MkdirAll(dir, dirMode))
	require.NoError(t, os.WriteFile(s.Path("web"), []byte("{"), fileMode))

	_, err := s.Load("web")

	require.Error(t, err)
	assert.Contains(t, err.Error(), s.Path("web"))
}

func TestStore_UnsafeDir(t *testing.T) {
	t.Parallel()
	s, dir := newStore(t)
	require.NoError(t, os.MkdirAll(dir, dirMode))
	require.NoError(t, os.Chmod(dir, 0o777))

	require.ErrorIs(t, s.Save("web", nil), ErrUnsafeDir)
	_, err := s.Load("web")
	require.ErrorIs(t, err, ErrUnsafeDir)
}

func TestDefaultDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user User
		want string
	}{
		{"root", User{UID: 0, Home: "/root"}, "/var/lib/restor"},
		{"xdg", User{UID: 1000, StateHome: "/s", Home: "/home/u"}, "/s/restor"},
		{
			"home",
			User{UID: 1000, StateHome: "rel", Home: "/home/u"},
			"/home/u/.local/state/restor",
		},
		{
			"fallback",
			User{UID: 1000, Fallback: "/run/user/1000/restor"},
			"/run/user/1000/restor",
		},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, DefaultDir(tt.user), tt.name)
	}
}
