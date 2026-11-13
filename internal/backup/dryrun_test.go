package backup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestManager_DryRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	svc := f.services(t, "key", "web")
	f.expectBackup("web", nil)
	f.restic.EXPECT().Backup(mock.Anything, "files", mock.Anything, mock.Anything).
		Return("", incompleteError{}).Once()
	j := job("web", svc)
	j.Settings.OnFailure, j.Settings.ForgetArgs = "/bin/failed", []string{"--keep-last=1"}
	files := fileJob(f, t, "files", "--keep-last=1")
	m := f.manager(t, Settings{
		DryRun: true, Prune: true, Check: true, Post: "/bin/post",
	}, j, files)

	require.NoError(t, m.Run(context.Background()))
	assert.Equal(t, []string{"backup web"}, f.rec.list(),
		"no locks, hooks, stop or start; the mocks fail the test on maintenance")
	assert.Empty(t, f.state.records)
}

func TestManager_DryRunFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.expectBackup("a", assert.AnError)
	a := fileJob(f, t, "a", "")
	b := requires(after(fileJob(f, t, "b", ""), "a"), "a")
	b.Settings.OnFailure = "/bin/failed"

	err := f.manager(t, Settings{DryRun: true}, a, b).Run(context.Background())

	require.ErrorIs(t, err, ErrJobsFailed)
	assert.Contains(t, err.Error(), "2 of 2", "b is skipped, without its hook")
}
