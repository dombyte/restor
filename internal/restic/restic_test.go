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
	c, err := New(Deps{Runner: r})
	require.NoError(t, err)
	return c, r
}

func TestNew_MissingRunner(t *testing.T) {
	t.Parallel()
	_, err := New(Deps{})
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
	c, err := New(Deps{Runner: r, Log: zerolog.New(&buf)})
	require.NoError(t, err)
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--exclude=*.tmp", "--tag",
		"web", "/a", "/b").Return([]byte(jsonStatus+"\n"+jsonSummary+"\n"), nil)

	id, err := c.Backup(context.Background(), "web", []string{"/a", "/b"},
		[]string{"--exclude=*.tmp"})

	require.NoError(t, err)
	assert.Equal(t, "9fdba01e", id, "the short ID")
	for _, field := range []string{
		`"files_new":1`, `"files_changed":2`, `"files_unmodified":3`, `"dirs_new":4`,
		`"bytes_added":3875`, `"files_processed":6`, `"bytes_processed":4096`,
		`"duration":1500`,
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
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag", "web", "/a").
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

func TestClient_BackupWithoutSnapshot(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, out string }{
		{name: "dry run", out: jsonSummary[:len(jsonSummary)-1] + `,"dry_run":true}`},
		{name: "no summary", out: "Warning: something\n" + jsonStatus},
		{name: "empty", out: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, r := newClient(t)
			r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag", "web", "/a").
				Return([]byte(tt.out), nil)

			id, err := c.Backup(context.Background(), "web", []string{"/a"}, nil)

			require.NoError(t, err)
			assert.Empty(t, id)
		})
	}
}

func TestClient_BackupOtherExitCodeFails(t *testing.T) {
	t.Parallel()
	c, r := newClient(t)
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag", "web", "/a").
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
	r.EXPECT().Run(mock.Anything, "restic", "backup", "--json", "--tag", "web", "/a").
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

			err := c.Prune(context.Background(), nil)

			require.ErrorIs(t, err, exitError(tt.code), "the runner's error is kept")
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want)
			}
			assert.True(t, strings.HasPrefix(err.Error(), tt.text), err.Error())
		})
	}
}
