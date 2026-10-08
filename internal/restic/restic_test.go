package restic

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/dombyte/restor/internal/restic/mocks"
)

func newClient(t *testing.T) (*Client, *mocks.MockRunner) {
	t.Helper()
	r := mocks.NewMockRunner(t)
	c, err := New(Deps{Runner: r})
	require.NoError(t, err)
	return c, r
}

func TestNew_MissingRunner(t *testing.T) {
	t.Parallel()
	_, err := New(Deps{})
	require.ErrorIs(t, err, ErrMissingDependency)
}

func TestClient_Backup(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--exclude=*.tmp", "--tag", "web",
		"/a", "/b").Return([]byte("Files: 3 new\nsnapshot 1a2b3c4d saved\n"), nil)

	id, err := c.Backup(context.Background(), "web", []string{"/a", "/b"},
		[]string{"--exclude=*.tmp"})

	require.NoError(t, err)
	assert.Equal(t, "1a2b3c4d", id)
}

func TestClient_BackupNoSources(t *testing.T) {
	t.Parallel()
	c, _ := newClient(t)
	_, err := c.Backup(context.Background(), "web", nil, nil)
	require.ErrorIs(t, err, ErrNoSources)
}

func TestClient_BackupFails(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--tag", "web", "/a").
		Return(nil, assert.AnError)

	_, err := c.Backup(context.Background(), "web", []string{"/a"}, nil)

	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "restic: backup web")
}

func TestClient_Forget(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "forget", "--dry-run", "--tag", "web",
		"--keep-daily", "7", "--keep-weekly", "4").Return(nil, nil)

	err := c.Forget(context.Background(), "web", " --keep-daily 7  --keep-weekly 4",
		[]string{"--dry-run"})

	require.NoError(t, err)
}

func TestClient_ForgetFails(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "forget", "--tag", "web").Return(nil, assert.AnError)
	require.ErrorIs(t, c.Forget(context.Background(), "web", "", nil), assert.AnError)
}

func TestClient_PruneAndUnlock(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	opts := []string{"--cache-dir=/tmp/c"}
	r.EXPECT().Run(mock.Anything, "restic", "--cache-dir=/tmp/c", "prune").Return(nil, nil)
	r.EXPECT().Run(mock.Anything, "restic", "--cache-dir=/tmp/c", "unlock").
		Return(nil, assert.AnError)

	require.NoError(t, c.Prune(context.Background(), opts))
	require.ErrorIs(t, c.Unlock(context.Background(), opts), assert.AnError)
	assert.Equal(t, []string{"--cache-dir=/tmp/c"}, opts, "options are not modified")
}

func TestClient_PruneFails(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "prune").Return(nil, assert.AnError)
	r.EXPECT().Run(mock.Anything, "restic", "unlock").Return(nil, nil)

	require.ErrorIs(t, c.Prune(context.Background(), nil), assert.AnError)
	require.NoError(t, c.Unlock(context.Background(), nil))
}

func TestParseSnapshotID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, output, want string
	}{
		{name: "saved line", output: "processed 3 files\nsnapshot abc123 saved\n", want: "abc123"},
		{name: "indented", output: "  snapshot abc123 saved  ", want: "abc123"},
		{name: "missing", output: "Fatal: unable to open repository", want: ""},
		{name: "other snapshot line", output: "snapshot abc123 loaded", want: ""},
		{name: "empty", output: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, parseSnapshotID(tt.output))
		})
	}
}
