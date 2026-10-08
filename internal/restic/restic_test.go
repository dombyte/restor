package restic

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"testing"

	"github.com/rs/zerolog"
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

// exitError fakes the exec error of a program that exited with code.
type exitError int

func (e exitError) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitError) ExitCode() int { return int(e) }

func TestClient_BackupIncomplete(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	r := mocks.NewMockRunner(t)
	c, err := New(Deps{Runner: r, Log: zerolog.New(&buf)})
	require.NoError(t, err)
	out := "error: open /a/secret: permission denied\n" +
		"Files:           1 new,     0 changed,     2 unmodified\n" +
		"Added to the repository: 1.5 KiB (900 B   stored)\n" +
		"processed 3 files, 4 KiB in 0:01\nsnapshot 1a2b3c4d saved\n" +
		"Warning: at least one source file could not be read\n"
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--tag", "web", "/a").
		Return([]byte(out), fmt.Errorf("command: restic: %w", exitError(3)))

	id, err := c.Backup(context.Background(), "web", []string{"/a"}, nil)

	assert.Equal(t, "1a2b3c4d", id)
	var incomplete *IncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.True(t, incomplete.Incomplete())
	assert.Contains(t, err.Error(), "snapshot incomplete")
	var exit exitError
	require.ErrorAs(t, err, &exit, "the runner's error is kept")
	assert.Contains(t, buf.String(), `"files":"1 new, 0 changed, 2 unmodified"`)
	assert.Contains(t, buf.String(), `"added":"1.5 KiB (900 B stored)"`)
	assert.Contains(t, buf.String(), `"processed":"3 files, 4 KiB in 0:01"`)
}

func TestClient_BackupOtherExitCodeFails(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--tag", "web", "/a").
		Return([]byte("snapshot 1a2b3c4d saved\n"), exitError(1))

	id, err := c.Backup(context.Background(), "web", []string{"/a"}, nil)

	assert.Empty(t, id)
	require.ErrorIs(t, err, exitError(1))
	var incomplete *IncompleteError
	assert.NotErrorAs(t, err, &incomplete)
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
