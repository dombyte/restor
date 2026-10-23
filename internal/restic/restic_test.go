package restic

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
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
	c, err := New(Settings{}, Deps{Runner: r})
	require.NoError(t, err)
	return c, r
}

func TestNew_MissingRunner(t *testing.T) {
	t.Parallel()
	_, err := New(Settings{}, Deps{})
	require.ErrorIs(t, err, ErrMissingDependency)
}

// Output of restic 0.19 `backup --json`, shortened.
const (
	jsonSummary = `{"message_type":"summary","files_new":1,"files_changed":2,` +
		`"files_unmodified":3,"dirs_new":4,"dirs_changed":0,"dirs_unmodified":6,` +
		`"data_added":3875,"total_files_processed":6,"total_bytes_processed":4096,` +
		`"total_duration":1.5,"snapshot_id":"9fdba01e5a142c12d6a3a1c83f9218b7973d232933fc"}`
	jsonStatus = `{"message_type":"status","percent_done":0.5}`
	jsonError  = `{"message_type":"error","error":{"message":"open /a/secret: ` +
		`permission denied"},"during":"archival","item":"/a/secret"}`
	jsonExit3 = `{"message_type":"exit_error","code":3,"message":"Warning: at least one ` +
		`source file could not be read"}`
)

func TestClient_Backup(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	r := mocks.NewMockRunner(t)
	c, err := New(Settings{GlobalArgs: []string{"--limit-upload=10"}},
		Deps{Runner: r, Log: zerolog.New(&buf)})
	require.NoError(t, err)
	r.EXPECT().Run(mock.Anything, "restic", "--limit-upload=10", "backup", "--json",
		"--exclude=*.tmp", "--tag=web", "--", "/a", "/b").
		Return([]byte(jsonStatus+"\n"+jsonSummary+"\n"), nil)

	id, err := c.Backup(context.Background(), "web", []string{"/a", "/b"},
		[]string{"--exclude=*.tmp"})

	require.NoError(t, err)
	assert.Equal(t, "9fdba01e", id, "the short ID")
	for _, field := range []string{
		`"files_new":1`, `"files_changed":2`, `"files_unmodified":3`, `"dirs_new":4`,
		`"bytes_added":3875`, `"files_processed":6`, `"bytes_processed":4096`,
		`"duration":"1.5s"`,
	} {
		assert.Contains(t, buf.String(), field)
	}
}

// exitError fakes the exec error of a program that exited with code.
type exitError int

func (e exitError) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitError) ExitCode() int { return int(e) }

func TestClient_BackupIncomplete(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	out := jsonError + "\n" + jsonSummary + "\n" + jsonExit3 + "\n"
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag=web", "--", "/a").
		Return([]byte(out), fmt.Errorf("command: restic: %w", exitError(3)))

	id, err := c.Backup(context.Background(), "web", []string{"/a"}, nil)

	assert.Equal(t, "9fdba01e", id)
	var incomplete *IncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.True(t, incomplete.Incomplete())
	assert.Contains(t, err.Error(), "snapshot incomplete")
	var exit exitError
	require.ErrorAs(t, err, &exit, "the runner's error is kept")
}

func TestClient_BackupIncompleteWithoutSummary(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag=web", "--", "/a").
		Return([]byte(jsonExit3), exitError(3))

	id, err := c.Backup(context.Background(), "web", []string{"/a"}, nil)

	assert.Empty(t, id)
	var incomplete *IncompleteError
	require.ErrorAs(t, err, &incomplete)
}

func TestClient_BackupWithoutSnapshot(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, out string }{
		{name: "dry run", out: jsonSummary[:len(jsonSummary)-1] + `,"dry_run":true}`},
		{name: "no summary", out: "Warning: something\n" + jsonStatus},
		{name: "dry run without ID", out: `{"message_type":"summary","dry_run":true}`},
		{name: "empty", out: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, r := newClient(t)
			r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag=web", "--", "/a").
				Return([]byte(tt.out), nil)

			id, err := c.Backup(context.Background(), "web", []string{"/a"}, nil)

			require.NoError(t, err)
			assert.Empty(t, id)
		})
	}
}

func TestClient_BackupUnchanged(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	// restic 0.19 with --skip-if-unchanged when nothing changed: no snapshot_id.
	out := `{"message_type":"summary","files_unmodified":2,"dirs_unmodified":9,` +
		`"total_files_processed":2,"total_bytes_processed":9,"total_duration":0.67}`
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag=web", "--", "/a").
		Return([]byte(out), nil)

	id, err := c.Backup(context.Background(), "web", []string{"/a"}, nil)

	assert.Empty(t, id)
	var unchanged *UnchangedError
	require.ErrorAs(t, err, &unchanged)
	assert.True(t, unchanged.Unchanged())
	assert.EqualError(t, err, "restic: backup web: nothing changed, no snapshot created")
}

func TestClient_BackupOtherExitCodeFails(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag=web", "--", "/a").
		Return([]byte(jsonSummary), exitError(1))

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
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag=web", "--", "/a").
		Return(nil, assert.AnError)

	_, err := c.Backup(context.Background(), "web", []string{"/a"}, nil)

	require.ErrorIs(t, err, assert.AnError)
	assert.Contains(t, err.Error(), "restic: backup web")
}

func TestClient_Forget(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "forget", "--group-by=host", "--tag=web",
		"--keep-daily=7").Return(nil, nil)

	require.NoError(t, c.Forget(context.Background(), "web", []string{"--keep-daily=7"}))
}

func TestClient_ForgetFails(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "forget", "--group-by=host", "--tag=web").
		Return(nil, assert.AnError)
	require.ErrorIs(t, c.Forget(context.Background(), "web", nil), assert.AnError)
}

func TestClient_PruneAndUnlock(t *testing.T) {
	t.Parallel()
	r := mocks.NewMockRunner(t)
	c, err := New(Settings{
		GlobalArgs: []string{"--option=s3.connections=5"}, PruneArgs: []string{"--max-unused=5%"},
	}, Deps{Runner: r})
	require.NoError(t, err)
	r.EXPECT().Run(mock.Anything, "restic", "--option=s3.connections=5", "prune",
		"--max-unused=5%").Return(nil, nil)
	r.EXPECT().Run(mock.Anything, "restic", "--option=s3.connections=5", "unlock").
		Return(nil, assert.AnError)

	require.NoError(t, c.Prune(context.Background()))
	require.ErrorIs(t, c.Unlock(context.Background()), assert.AnError)
}

func TestClient_PruneFails(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "prune").Return(nil, assert.AnError)
	r.EXPECT().Run(mock.Anything, "restic", "unlock").Return(nil, nil)

	require.ErrorIs(t, c.Prune(context.Background()), assert.AnError)
	require.NoError(t, c.Unlock(context.Background()))
}

func TestClient_Version(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "version").
		Return([]byte("restic 0.17.3 compiled with go1.23.1 on linux/amd64\n"), nil).Once()
	r.EXPECT().Run(mock.Anything, "restic", "version").Return(nil, assert.AnError).Once()

	v, err := c.Version(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Version{Major: 0, Minor: 17, Patch: 3}, v)
	_, err = c.Version(context.Background())
	require.ErrorIs(t, err, assert.AnError)
}

func TestClient_ExitCodeCauses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		code int
		want error
		text string
	}{
		{code: 10, want: errNoRepository, text: "restic: prune: repository does not exist"},
		{code: 11, want: errLocked, text: "restic: prune: repository is locked"},
		{code: 12, want: errWrongPassword, text: "restic: prune: wrong password"},
		{code: 1, text: "restic: prune: exit status 1"},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.code), func(t *testing.T) {
			t.Parallel()
			c, r := newClient(t)
			r.EXPECT().Run(mock.Anything, "restic", "prune").Return(nil, exitError(tt.code))

			err := c.Prune(context.Background())

			require.ErrorIs(t, err, exitError(tt.code), "the runner's error is kept")
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want)
			}
			assert.True(t, strings.HasPrefix(err.Error(), tt.text), err.Error())
		})
	}
}
